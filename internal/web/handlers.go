package web

import (
	"net/http"
	"strconv"
	"strings"

	"planner/internal/store"
)

type handlers struct {
	st  store.Store
	cfg Config
}

// ---- DTOs (the JSON wire shapes shared with the CLI and React) ----

type planSummaryDTO struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	Project       string `json:"project"`
	LatestVersion int    `json:"latest_version"`
	OpenComments  int    `json:"open_comments"`
	Shared        bool   `json:"shared"`
}

type planMetaDTO struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Status           string `json:"status"`
	Project          string `json:"project"`
	Versions         []int  `json:"versions"`
	Latest           int    `json:"latest"`
	ShareID          string `json:"share_id,omitempty"` // owner view only
	ShareAllVersions *bool  `json:"share_all_versions,omitempty"`
	ShareVersions    []int  `json:"share_versions,omitempty"`
}

type replyDTO struct {
	ID            string `json:"id"`
	Author        string `json:"author"`
	AuthorName    string `json:"author_name,omitempty"`
	AuthorPicture string `json:"author_picture,omitempty"`
	Own           bool   `json:"own,omitempty"` // authored by the requesting user
	Body          string `json:"body"`
}

type commentDTO struct {
	ID            string     `json:"id"`
	LineStart     int        `json:"line_start"`
	LineEnd       int        `json:"line_end"`
	WholeFile     bool       `json:"whole_file"`
	Quote         string     `json:"quote"`
	Body          string     `json:"body"`
	Status        string     `json:"status"`
	AuthorName    string     `json:"author_name,omitempty"`
	AuthorPicture string     `json:"author_picture,omitempty"`
	Own           bool       `json:"own,omitempty"` // authored by the requesting user
	Replies       []replyDTO `json:"replies"`
}

// fileRefDTO is one referenced-file metadata entry on the version view: enough
// for the frontend to decorate a reference token and fetch its content by sha.
// It carries no content — content is fetched lazily via GET /api/files/{sha}.
type fileRefDTO struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	SHA      string `json:"sha"`
}

type versionViewDTO struct {
	PlanID           string       `json:"plan_id"`
	Title            string       `json:"title"`
	Number           int          `json:"number"`
	Content          string       `json:"content"`
	Versions         []int        `json:"versions"`
	Latest           int          `json:"latest"`
	Role             string       `json:"role"`               // "owner" | "shared"
	ShareID          string       `json:"share_id,omitempty"` // owner view only
	ShareAllVersions *bool        `json:"share_all_versions,omitempty"`
	ShareVersions    []int        `json:"share_versions,omitempty"`
	Comments         []commentDTO `json:"comments"`
	Carryover        []commentDTO `json:"carryover"`
	PrevNumber       int          `json:"prev_number"`
	Files            []fileRefDTO `json:"files"` // referenced-file metadata (no content)
}

// isOwn reports whether a row authored by authorID belongs to the requesting
// user (actor). Unattributed rows are nobody's "own".
func isOwn(authorID, actor string) bool {
	return authorID != "" && authorID == actor
}

// toCommentDTO converts a store comment to its wire shape. Stored ids are
// composite ("<plan_id>_c_<local>"); the wire carries the short local form, the
// inverse of the handlers' planID+"_"+cid reassembly. actor is the requesting
// user, used to flag the rows they authored.
func toCommentDTO(planID, actor string, c store.Comment) commentDTO {
	replies := make([]replyDTO, 0, len(c.Replies))
	for _, r := range c.Replies {
		replies = append(replies, replyDTO{
			ID:            strings.TrimPrefix(r.ID, planID+"_"),
			Author:        r.Author,
			AuthorName:    r.AuthorName,
			AuthorPicture: r.AuthorPicture,
			Own:           isOwn(r.AuthorID, actor),
			Body:          r.Body,
		})
	}
	return commentDTO{
		ID:            strings.TrimPrefix(c.ID, planID+"_"),
		LineStart:     c.LineStart,
		LineEnd:       c.LineEnd,
		WholeFile:     c.WholeFile(),
		Quote:         c.Quote,
		Body:          c.Body,
		Status:        c.Status,
		AuthorName:    c.AuthorName,
		AuthorPicture: c.AuthorPicture,
		Own:           isOwn(c.AuthorID, actor),
		Replies:       replies,
	}
}

// toCommentDTOs builds comment DTOs from store comments (each carrying its own
// reply thread in Comment.Replies).
func toCommentDTOs(planID, actor string, cs []store.Comment) []commentDTO {
	out := make([]commentDTO, 0, len(cs))
	for _, c := range cs {
		out = append(out, toCommentDTO(planID, actor, c))
	}
	return out
}

func boolPtr(v bool) *bool { return &v }

// ---- Plan access resolution (canonical vs share ids) ----

const (
	roleOwner  = "owner"
	roleShared = "shared"
)

// resolvePlan maps the {id} path value — a canonical plan id, or a share id
// standing in for one — to a store scoped for that access, the canonical plan
// id, and the caller's role. A share id resolves on the root store (possessing
// the id is the authorization; the login gate has already run) to a
// plan-grant-scoped store; anything else gets the usual owner scoping. An
// unknown or revoked share id is store.ErrNotFound.
func (h *handlers) resolvePlan(r *http.Request) (st store.Store, planID, role string, err error) {
	pid := r.PathValue("id")
	if strings.HasPrefix(pid, "share_") {
		canonical, err := h.st.ResolveShareID(pid)
		if err != nil {
			return nil, "", "", err
		}
		// The owner following their own share link gets owner access (the plan
		// resolves under their owner scope), so the SPA can bounce them to the
		// canonical URL instead of showing the stripped-down shared view.
		owned := h.store(r)
		if _, err := owned.GetPlan(canonical); err == nil {
			return owned, canonical, roleOwner, nil
		}
		return h.st.WithPlanGrant(canonical), canonical, roleShared, nil
	}
	return h.store(r), pid, roleOwner, nil
}

// requireOwner is resolvePlan's owner-only counterpart: a share id is refused
// with a 403 (the caller can already view the plan, so its existence is no
// secret) and the usual owner-scoped store is returned otherwise. The bool
// reports whether the caller may proceed.
func (h *handlers) requireOwner(w http.ResponseWriter, r *http.Request) (store.Store, string, bool) {
	pid := r.PathValue("id")
	if strings.HasPrefix(pid, "share_") {
		writeJSONError(w, http.StatusForbidden, "owner only — a share link grants view and comment access")
		return nil, "", false
	}
	return h.store(r), pid, true
}

// actorID returns the authenticated user id from the request context, or ""
// in no-auth mode — the value stored as comment/reply author_id.
func actorID(r *http.Request) string {
	uid, _ := r.Context().Value(userIDKey{}).(string)
	return uid
}

// ---- Plan endpoints ----

func (h *handlers) apiListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.store(r).ListPlans()
	if err != nil {
		writeServerError(w, err)
		return
	}
	out := make([]planSummaryDTO, 0, len(plans))
	for _, p := range plans {
		out = append(out, planSummaryDTO{
			ID:            p.ID,
			Title:         p.Title,
			Status:        p.Status,
			Project:       p.Project,
			LatestVersion: p.LatestVersion,
			OpenComments:  p.OpenComments,
			Shared:        p.ShareID != "",
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) apiCreatePlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title   string               `json:"title"`
		Content string               `json:"content"`
		Project string               `json:"project"`
		Files   []store.FileSnapshot `json:"files"`
	}
	if !readJSONLimit(w, r, &in, maxPlanPostBytes) {
		return
	}
	if in.Title == "" {
		writeJSONError(w, http.StatusBadRequest, "title is required")
		return
	}
	if in.Project == "" {
		in.Project = store.NoProject
	}
	p, v, err := h.store(r).CreatePlan(in.Title, in.Content, in.Project, in.Files)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"plan_id": p.ID, "number": v.Number})
}

func (h *handlers) apiPlanMeta(w http.ResponseWriter, r *http.Request) {
	st, planID, role, err := h.resolvePlan(r)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	plan, err := st.GetPlan(planID)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	latest := 0
	if len(plan.Versions) > 0 {
		latest = plan.Versions[len(plan.Versions)-1]
	}
	meta := planMetaDTO{ID: plan.ID, Title: plan.Title, Status: plan.Status, Project: plan.Project, Versions: plan.Versions, Latest: latest}
	if role == roleOwner {
		meta.ShareID = plan.ShareID
		meta.ShareAllVersions = boolPtr(plan.ShareAllVersions)
		meta.ShareVersions = plan.ShareVersions
	}
	writeJSON(w, http.StatusOK, meta)
}

func (h *handlers) apiAddVersion(w http.ResponseWriter, r *http.Request) {
	st, id, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	var in struct {
		Content string               `json:"content"`
		Files   []store.FileSnapshot `json:"files"`
	}
	if !readJSONLimit(w, r, &in, maxPlanPostBytes) {
		return
	}
	v, err := st.AddVersion(id, in.Content, in.Files)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"plan_id": id, "number": v.Number})
}

// resolveVersionNumber maps the {n} path value (an integer or "latest") to a
// concrete version number, using the already-fetched plan's version list to
// resolve "latest" without a second store read.
func (h *handlers) resolveVersionNumber(plan store.Plan, n string) (int, error) {
	if n == "latest" {
		if len(plan.Versions) == 0 {
			return 0, store.ErrNotFound
		}
		return plan.Versions[len(plan.Versions)-1], nil
	}
	num, err := strconv.Atoi(n)
	if err != nil {
		return 0, errBadVersion
	}
	return num, nil
}

func (h *handlers) apiVersionView(w http.ResponseWriter, r *http.Request) {
	st, id, role, err := h.resolvePlan(r)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	plan, err := st.GetPlan(id)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	n, err := h.resolveVersionNumber(plan, r.PathValue("n"))
	if err != nil {
		writeVersionErr(w, err)
		return
	}
	version, err := st.GetVersion(id, n)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	comments, err := st.ListComments(version.ID, false)
	if err != nil {
		writeServerError(w, err)
		return
	}
	fileList, err := st.GetVersionFileList(version.ID)
	if err != nil {
		writeServerError(w, err)
		return
	}

	latest := plan.Versions[len(plan.Versions)-1]
	var carryover []store.Comment
	prevNumber := n - 1
	if n == latest && prevNumber >= 1 {
		if prev, err := st.GetVersion(id, prevNumber); err == nil {
			if carryover, err = st.ListComments(prev.ID, true); err != nil {
				writeServerError(w, err)
				return
			}
		}
	}

	view := versionViewDTO{
		PlanID:     plan.ID,
		Title:      plan.Title,
		Number:     version.Number,
		Content:    version.Content,
		Versions:   plan.Versions,
		Latest:     latest,
		Role:       role,
		Comments:   toCommentDTOs(id, actorID(r), comments),
		Carryover:  toCommentDTOs(id, actorID(r), carryover),
		PrevNumber: prevNumber,
		Files:      toFileRefDTOs(fileList),
	}
	if role == roleOwner {
		view.ShareID = plan.ShareID
		view.ShareAllVersions = boolPtr(plan.ShareAllVersions)
		view.ShareVersions = plan.ShareVersions
	}
	writeJSON(w, http.StatusOK, view)
}

// toFileRefDTOs converts store FileRefs to their wire DTOs (metadata only).
func toFileRefDTOs(refs []store.FileRef) []fileRefDTO {
	out := make([]fileRefDTO, 0, len(refs))
	for _, f := range refs {
		out = append(out, fileRefDTO{Path: f.Path, Language: f.Language, SHA: f.SHA})
	}
	return out
}

// ---- Comment endpoints ----

func (h *handlers) apiAddComment(w http.ResponseWriter, r *http.Request) {
	st, id, _, err := h.resolvePlan(r)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	plan, err := st.GetPlan(id)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	n, err := h.resolveVersionNumber(plan, r.PathValue("n"))
	if err != nil {
		writeVersionErr(w, err)
		return
	}
	version, err := st.GetVersion(id, n)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	var in struct {
		LineStart int    `json:"line_start"`
		LineEnd   int    `json:"line_end"`
		Quote     string `json:"quote"`
		Body      string `json:"body"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Body == "" {
		writeJSONError(w, http.StatusBadRequest, "body is required")
		return
	}
	c, err := st.AddComment(id, version.ID, in.LineStart, in.LineEnd, in.Quote, in.Body, actorID(r))
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCommentDTO(id, actorID(r), c))
}

// fullCommentID reassembles a stored composite comment id from the resolved
// plan id and the short {cid} path value. A cid presented under the wrong plan
// forms a key that does not exist, so membership needs no separate check.
func fullCommentID(planID string, r *http.Request) string {
	return planID + "_" + r.PathValue("cid")
}

func (h *handlers) apiResolveComment(w http.ResponseWriter, r *http.Request) {
	h.setStatus(w, r, store.StatusResolved)
}

func (h *handlers) apiReopenComment(w http.ResponseWriter, r *http.Request) {
	h.setStatus(w, r, store.StatusOpen)
}

func (h *handlers) setStatus(w http.ResponseWriter, r *http.Request, status string) {
	st, planID, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	if err := st.SetCommentStatus(fullCommentID(planID, r), status); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) apiKeepComment(w http.ResponseWriter, r *http.Request) {
	st, planID, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	if err := st.CarryComment(fullCommentID(planID, r)); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteAuthorConstraint maps the caller's role to DeleteComment/DeleteReply's
// author restriction: the owner moderates anything (""), a shared viewer may
// remove only what they authored. The shared role exists only in authed mode
// (resolvePlan upgrades to owner in no-auth), so the actor id is never empty
// when constrained.
func deleteAuthorConstraint(r *http.Request, role string) string {
	if role == roleShared {
		return actorID(r)
	}
	return ""
}

func (h *handlers) apiDeleteComment(w http.ResponseWriter, r *http.Request) {
	st, planID, role, err := h.resolvePlan(r)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	if err := st.DeleteComment(fullCommentID(planID, r), deleteAuthorConstraint(r, role)); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) apiDeleteReply(w http.ResponseWriter, r *http.Request) {
	st, planID, role, err := h.resolvePlan(r)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	if err := st.DeleteReply(planID+"_"+r.PathValue("rid"), deleteAuthorConstraint(r, role)); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) apiAddReply(w http.ResponseWriter, r *http.Request) {
	st, planID, _, err := h.resolvePlan(r)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	var in struct {
		Author string `json:"author"`
		Body   string `json:"body"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Author != store.AuthorHuman && in.Author != store.AuthorAgent {
		writeJSONError(w, http.StatusBadRequest, "author must be \"human\" or \"agent\"")
		return
	}
	if in.Body == "" {
		writeJSONError(w, http.StatusBadRequest, "body is required")
		return
	}
	rep, err := st.AddReply(fullCommentID(planID, r), in.Author, in.Body, actorID(r))
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, replyDTO{
		ID:            strings.TrimPrefix(rep.ID, planID+"_"),
		Author:        rep.Author,
		AuthorName:    rep.AuthorName,
		AuthorPicture: rep.AuthorPicture,
		Own:           isOwn(rep.AuthorID, actorID(r)),
		Body:          rep.Body,
	})
}

// ---- Sharing (owner only) ----

// apiCreateShare mints (or returns the existing) share id for a plan. With an
// empty body it preserves the legacy create-or-get all-history behavior. With a
// JSON body, it also updates the share policy to either all versions or a
// selected version set.
func (h *handlers) apiCreateShare(w http.ResponseWriter, r *http.Request) {
	st, planID, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	ensureShare := func() {
		sid, err := st.EnsureShareID(planID)
		if err != nil {
			writeNotFoundOr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"share_id": sid})
	}
	if r.ContentLength == 0 {
		ensureShare()
		return
	}
	var in struct {
		AllVersions bool  `json:"all_versions"`
		Versions    []int `json:"versions"`
	}
	if ok, empty := readJSONLimitOrEmpty(w, r, &in, maxBodyBytes); empty {
		ensureShare()
		return
	} else if !ok {
		return
	}
	if !in.AllVersions {
		if len(in.Versions) == 0 {
			writeJSONError(w, http.StatusBadRequest, "versions is required when all_versions is false")
			return
		}
		for _, n := range in.Versions {
			if n <= 0 {
				writeJSONError(w, http.StatusBadRequest, "versions must be positive")
				return
			}
		}
	}
	sid, err := st.SetSharePolicy(planID, in.AllVersions, in.Versions)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"share_id": sid})
}

// apiRevokeShare nulls the plan's share id; outstanding links then 404.
func (h *handlers) apiRevokeShare(w http.ResponseWriter, r *http.Request) {
	st, planID, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	if err := st.ClearShareID(planID); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiSetPlanStatus moves a plan through its lifecycle (active|completed|stashed).
// The target is supplied in the body, so an unknown value is a 400.
func (h *handlers) apiSetPlanStatus(w http.ResponseWriter, r *http.Request) {
	st, id, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !store.ValidPlanStatus(in.Status) {
		writeJSONError(w, http.StatusBadRequest, "status must be one of active, completed, stashed")
		return
	}
	if err := st.SetPlanStatus(id, in.Status); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiSetPlanProject re-assigns the project a plan is grouped under. The value is
// free-form (a folder path or a repo identity); a blank value clears it back to
// the "No Project" placeholder.
func (h *handlers) apiSetPlanProject(w http.ResponseWriter, r *http.Request) {
	st, id, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	var in struct {
		Project string `json:"project"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	project := strings.TrimSpace(in.Project)
	if project == "" {
		project = store.NoProject
	}
	if err := st.SetPlanProject(id, project); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Referenced-file content (lazy, content-addressed) ----

type fileContentDTO struct {
	Content string `json:"content"`
}

// apiFile serves one referenced file's content by its sha. Because content is
// addressed by hash it never changes, so the response is cached immutably and
// dedupes across versions and plans (same sha → same URL → one browser-cache
// entry). The sha must be lowercase hex; an unknown sha is a 404.
func (h *handlers) apiFile(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha")
	if !isHex(sha) {
		writeJSONError(w, http.StatusBadRequest, "invalid sha")
		return
	}
	content, err := h.st.GetBlob(sha)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	writeJSON(w, http.StatusOK, fileContentDTO{Content: content})
}

// isHex reports whether s is a non-empty string of lowercase hex digits.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ---- Plan deletion (human-only; not exposed to the agent CLI) ----

func (h *handlers) apiDeletePlan(w http.ResponseWriter, r *http.Request) {
	st, id, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	if err := st.DeletePlan(id); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
