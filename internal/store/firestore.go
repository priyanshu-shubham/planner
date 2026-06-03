package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"planner/internal/id"
)

// firestoreStore is the Firestore-backed Store implementation, used when planner
// runs on Cloud Run (where the local filesystem is ephemeral and per-instance).
//
// Data model: four flat top-level collections — plans, versions, comments,
// replies — each document keyed by the existing app id (pl_…, v_…, c_…, r_…).
// Comments carry a denormalized plan_id so "open comments for a plan" is a single
// indexed query + Count() aggregation rather than a join across versions.
type firestoreStore struct {
	client *firestore.Client
	ctx    context.Context
}

// ---- document shapes (Firestore field names mirror the SQLite columns) ----

type planDoc struct {
	Title     string    `firestore:"title"`
	Status    string    `firestore:"status"`
	Project   string    `firestore:"project"`
	CreatedAt time.Time `firestore:"created_at"`
}

type versionDoc struct {
	PlanID    string    `firestore:"plan_id"`
	Number    int       `firestore:"number"`
	Content   string    `firestore:"content"`
	CreatedAt time.Time `firestore:"created_at"`
}

type commentDoc struct {
	VersionID string    `firestore:"version_id"`
	PlanID    string    `firestore:"plan_id"` // denormalized for plan-scoped queries
	LineStart int       `firestore:"line_start"`
	LineEnd   int       `firestore:"line_end"`
	Quote     string    `firestore:"quote"`
	Body      string    `firestore:"body"`
	Status    string    `firestore:"status"`
	CreatedAt time.Time `firestore:"created_at"`
}

type replyDoc struct {
	CommentID string    `firestore:"comment_id"`
	Author    string    `firestore:"author"`
	Body      string    `firestore:"body"`
	CreatedAt time.Time `firestore:"created_at"`
}

// OpenFirestore connects to the named Firestore database in projectID. Pass
// "(default)" for the default database. When FIRESTORE_EMULATOR_HOST is set the
// client talks to the local emulator automatically.
func OpenFirestore(ctx context.Context, projectID, database string) (Store, error) {
	client, err := firestore.NewClientWithDatabase(ctx, projectID, database)
	if err != nil {
		return nil, err
	}
	return &firestoreStore{client: client, ctx: ctx}, nil
}

func (s *firestoreStore) plans() *firestore.CollectionRef    { return s.client.Collection("plans") }
func (s *firestoreStore) versions() *firestore.CollectionRef { return s.client.Collection("versions") }
func (s *firestoreStore) comments() *firestore.CollectionRef { return s.client.Collection("comments") }
func (s *firestoreStore) replies() *firestore.CollectionRef  { return s.client.Collection("replies") }

// mapErr translates a Firestore "document not found" into the shared ErrNotFound.
func mapErr(err error) error {
	if status.Code(err) == codes.NotFound {
		return ErrNotFound
	}
	return err
}

func (s *firestoreStore) Close() error { return s.client.Close() }

// ---- plans / versions ----

func (s *firestoreStore) CreatePlan(title, content, project string) (Plan, Version, error) {
	p := Plan{ID: id.New("pl"), Title: title, Status: PlanActive, Project: project, CreatedAt: now()}
	v := Version{ID: id.New("v"), PlanID: p.ID, Number: 1, Content: content, CreatedAt: now()}

	bw := s.client.Batch()
	bw.Set(s.plans().Doc(p.ID), planDoc{Title: p.Title, Status: p.Status, Project: p.Project, CreatedAt: p.CreatedAt})
	bw.Set(s.versions().Doc(v.ID), versionDoc{PlanID: v.PlanID, Number: v.Number, Content: v.Content, CreatedAt: v.CreatedAt})
	if _, err := bw.Commit(s.ctx); err != nil {
		return Plan{}, Version{}, err
	}
	return p, v, nil
}

func (s *firestoreStore) GetPlan(planID string) (Plan, error) {
	snap, err := s.plans().Doc(planID).Get(s.ctx)
	if err != nil {
		return Plan{}, mapErr(err)
	}
	var d planDoc
	if err := snap.DataTo(&d); err != nil {
		return Plan{}, err
	}
	// Version numbers are contiguous 1..latest (versions are never deleted
	// individually), so generate the list from the latest number.
	_, latest, err := s.latestVersion(planID)
	if err != nil && err != ErrNotFound {
		return Plan{}, err
	}
	var versions []int
	for n := 1; n <= latest; n++ {
		versions = append(versions, n)
	}
	return Plan{ID: planID, Title: d.Title, Status: d.Status, Project: d.Project, CreatedAt: d.CreatedAt, Versions: versions}, nil
}

func (s *firestoreStore) SetPlanStatus(planID, status string) error {
	_, err := s.plans().Doc(planID).Update(s.ctx, []firestore.Update{{Path: "status", Value: status}})
	return mapErr(err) // Update on a missing doc returns NotFound
}

func (s *firestoreStore) AddVersion(planID, content string) (Version, error) {
	var v Version
	err := s.client.RunTransaction(s.ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		// All reads must precede writes in a Firestore transaction.
		if _, err := tx.Get(s.plans().Doc(planID)); err != nil {
			return mapErr(err)
		}
		docs, err := tx.Documents(
			s.versions().Where("plan_id", "==", planID).OrderBy("number", firestore.Desc).Limit(1)).GetAll()
		if err != nil {
			return err
		}
		next := 1
		if len(docs) > 0 {
			var vd versionDoc
			if err := docs[0].DataTo(&vd); err != nil {
				return err
			}
			next = vd.Number + 1
		}
		v = Version{ID: id.New("v"), PlanID: planID, Number: next, Content: content, CreatedAt: now()}
		return tx.Set(s.versions().Doc(v.ID),
			versionDoc{PlanID: v.PlanID, Number: v.Number, Content: v.Content, CreatedAt: v.CreatedAt})
	})
	if err != nil {
		return Version{}, err
	}
	return v, nil
}

func (s *firestoreStore) GetVersion(planID string, number int) (Version, error) {
	iter := s.versions().Where("plan_id", "==", planID).Where("number", "==", number).Limit(1).Documents(s.ctx)
	defer iter.Stop()
	snap, err := iter.Next()
	if err == iterator.Done {
		return Version{}, ErrNotFound
	}
	if err != nil {
		return Version{}, err
	}
	var vd versionDoc
	if err := snap.DataTo(&vd); err != nil {
		return Version{}, err
	}
	return Version{ID: snap.Ref.ID, PlanID: vd.PlanID, Number: vd.Number, Content: vd.Content, CreatedAt: vd.CreatedAt}, nil
}

// latestVersion returns the highest-numbered version's id and number, or
// ErrNotFound if the plan has no versions.
func (s *firestoreStore) latestVersion(planID string) (string, int, error) {
	iter := s.versions().Where("plan_id", "==", planID).OrderBy("number", firestore.Desc).Limit(1).Documents(s.ctx)
	defer iter.Stop()
	snap, err := iter.Next()
	if err == iterator.Done {
		return "", 0, ErrNotFound
	}
	if err != nil {
		return "", 0, err
	}
	var vd versionDoc
	if err := snap.DataTo(&vd); err != nil {
		return "", 0, err
	}
	return snap.Ref.ID, vd.Number, nil
}

func (s *firestoreStore) ListPlans() ([]PlanSummary, error) {
	iter := s.plans().OrderBy("created_at", firestore.Desc).Documents(s.ctx)
	defer iter.Stop()
	var out []PlanSummary
	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var d planDoc
		if err := snap.DataTo(&d); err != nil {
			return nil, err
		}
		ps := PlanSummary{Plan: Plan{ID: snap.Ref.ID, Title: d.Title, Status: d.Status, Project: d.Project, CreatedAt: d.CreatedAt}}
		// latest version number (0 if none, matching SQLite's COALESCE)
		if _, num, err := s.latestVersion(snap.Ref.ID); err == nil {
			ps.LatestVersion = num
		} else if err != ErrNotFound {
			return nil, err
		}
		cnt, err := s.openCommentCount(snap.Ref.ID)
		if err != nil {
			return nil, err
		}
		ps.OpenComments = cnt
		out = append(out, ps)
	}
	return out, nil
}

// openCommentCount returns the number of open comments on a plan via a Firestore
// Count() aggregation over the denormalized plan_id + status index.
func (s *firestoreStore) openCommentCount(planID string) (int, error) {
	q := s.comments().Where("plan_id", "==", planID).Where("status", "==", StatusOpen)
	res, err := q.NewAggregationQuery().WithCount("count").Get(s.ctx)
	if err != nil {
		return 0, err
	}
	v, ok := res["count"]
	if !ok {
		return 0, fmt.Errorf("count aggregation result missing")
	}
	cv, ok := v.(*firestorepb.Value)
	if !ok {
		return 0, fmt.Errorf("unexpected aggregation value type %T", v)
	}
	return int(cv.GetIntegerValue()), nil
}

// ---- comments / replies ----

func (s *firestoreStore) ListComments(versionID string, openOnly bool) ([]Comment, error) {
	iter := s.comments().Where("version_id", "==", versionID).
		OrderBy("line_start", firestore.Asc).OrderBy("created_at", firestore.Asc).Documents(s.ctx)
	defer iter.Stop()
	var out []Comment
	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var cd commentDoc
		if err := snap.DataTo(&cd); err != nil {
			return nil, err
		}
		if openOnly && cd.Status != StatusOpen {
			continue
		}
		c := Comment{
			ID: snap.Ref.ID, VersionID: cd.VersionID, LineStart: cd.LineStart, LineEnd: cd.LineEnd,
			Quote: cd.Quote, Body: cd.Body, Status: cd.Status, CreatedAt: cd.CreatedAt,
		}
		reps, err := s.repliesForComment(c.ID)
		if err != nil {
			return nil, err
		}
		c.Replies = reps
		out = append(out, c)
	}
	return out, nil
}

// repliesForComment returns a comment's replies oldest first. Firestore is
// queried by comment_id alone (a single-field index) and sorted in memory, so no
// per-comment composite index is needed.
func (s *firestoreStore) repliesForComment(commentID string) ([]Reply, error) {
	iter := s.replies().Where("comment_id", "==", commentID).Documents(s.ctx)
	defer iter.Stop()
	var out []Reply
	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var rd replyDoc
		if err := snap.DataTo(&rd); err != nil {
			return nil, err
		}
		out = append(out, Reply{ID: snap.Ref.ID, CommentID: rd.CommentID, Author: rd.Author, Body: rd.Body, CreatedAt: rd.CreatedAt})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *firestoreStore) AddComment(versionID string, lineStart, lineEnd int, quote, body string) (Comment, error) {
	// Look up the version to denormalize its plan_id onto the comment.
	vsnap, err := s.versions().Doc(versionID).Get(s.ctx)
	if err != nil {
		return Comment{}, mapErr(err)
	}
	var vd versionDoc
	if err := vsnap.DataTo(&vd); err != nil {
		return Comment{}, err
	}
	c := Comment{
		ID: id.New("c"), VersionID: versionID, LineStart: lineStart, LineEnd: lineEnd,
		Quote: quote, Body: body, Status: StatusOpen, CreatedAt: now(),
	}
	cd := commentDoc{
		VersionID: versionID, PlanID: vd.PlanID, LineStart: lineStart, LineEnd: lineEnd,
		Quote: quote, Body: body, Status: StatusOpen, CreatedAt: c.CreatedAt,
	}
	if _, err := s.comments().Doc(c.ID).Set(s.ctx, cd); err != nil {
		return Comment{}, err
	}
	return c, nil
}

func (s *firestoreStore) SetCommentStatus(commentID, status string) error {
	_, err := s.comments().Doc(commentID).Update(s.ctx, []firestore.Update{{Path: "status", Value: status}})
	return mapErr(err) // Update on a missing doc returns NotFound
}

func (s *firestoreStore) DeleteComment(commentID string) error {
	// Firestore Delete is idempotent (no error on a missing doc), so check first
	// to honor the ErrNotFound contract.
	if _, err := s.comments().Doc(commentID).Get(s.ctx); err != nil {
		return mapErr(err)
	}
	bw := s.client.Batch()
	if err := s.deleteRepliesOf(bw, commentID); err != nil {
		return err
	}
	bw.Delete(s.comments().Doc(commentID))
	_, err := bw.Commit(s.ctx)
	return err
}

func (s *firestoreStore) DeletePlan(planID string) error {
	if _, err := s.plans().Doc(planID).Get(s.ctx); err != nil {
		return mapErr(err)
	}
	bw := s.client.Batch()

	// Comments (and their replies) via the denormalized plan_id.
	cIter := s.comments().Where("plan_id", "==", planID).Documents(s.ctx)
	for {
		snap, err := cIter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			cIter.Stop()
			return err
		}
		if err := s.deleteRepliesOf(bw, snap.Ref.ID); err != nil {
			cIter.Stop()
			return err
		}
		bw.Delete(snap.Ref)
	}
	cIter.Stop()

	// Versions.
	vIter := s.versions().Where("plan_id", "==", planID).Documents(s.ctx)
	for {
		snap, err := vIter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			vIter.Stop()
			return err
		}
		bw.Delete(snap.Ref)
	}
	vIter.Stop()

	bw.Delete(s.plans().Doc(planID))
	_, err := bw.Commit(s.ctx)
	return err
}

// deleteRepliesOf queues deletions for all replies on a comment into bw.
func (s *firestoreStore) deleteRepliesOf(bw *firestore.WriteBatch, commentID string) error {
	iter := s.replies().Where("comment_id", "==", commentID).Documents(s.ctx)
	defer iter.Stop()
	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			return nil
		}
		if err != nil {
			return err
		}
		bw.Delete(snap.Ref)
	}
}

func (s *firestoreStore) AddReply(commentID, author, body string) (Reply, error) {
	if _, err := s.comments().Doc(commentID).Get(s.ctx); err != nil {
		return Reply{}, mapErr(err)
	}
	r := Reply{ID: id.New("r"), CommentID: commentID, Author: author, Body: body, CreatedAt: now()}
	if _, err := s.replies().Doc(r.ID).Set(s.ctx,
		replyDoc{CommentID: commentID, Author: author, Body: body, CreatedAt: r.CreatedAt}); err != nil {
		return Reply{}, err
	}
	return r, nil
}

// CarryComment copies an existing comment onto the plan's latest version as a
// whole-file comment (with its reply thread) and marks the original resolved.
func (s *firestoreStore) CarryComment(commentID string) error {
	osnap, err := s.comments().Doc(commentID).Get(s.ctx)
	if err != nil {
		return mapErr(err)
	}
	var od commentDoc
	if err := osnap.DataTo(&od); err != nil {
		return err
	}
	latestID, _, err := s.latestVersion(od.PlanID)
	if err != nil {
		return err
	}
	reps, err := s.repliesForComment(commentID)
	if err != nil {
		return err
	}

	newCID := id.New("c")
	bw := s.client.Batch()
	bw.Set(s.comments().Doc(newCID), commentDoc{
		VersionID: latestID, PlanID: od.PlanID, LineStart: 0, LineEnd: 0,
		Quote: "", Body: od.Body, Status: StatusOpen, CreatedAt: now(),
	})
	for _, r := range reps {
		bw.Set(s.replies().Doc(id.New("r")),
			replyDoc{CommentID: newCID, Author: r.Author, Body: r.Body, CreatedAt: r.CreatedAt})
	}
	bw.Update(s.comments().Doc(commentID), []firestore.Update{{Path: "status", Value: StatusResolved}})
	_, err = bw.Commit(s.ctx)
	return err
}
