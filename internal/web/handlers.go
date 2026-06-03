package web

import (
	"net/http"
	"strconv"

	"planner/internal/store"
)

type handlers struct {
	st store.Store
}

// ---- DTOs (the JSON wire shapes shared with the CLI and React) ----

type planSummaryDTO struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	Project       string `json:"project"`
	LatestVersion int    `json:"latest_version"`
	OpenComments  int    `json:"open_comments"`
}

type planMetaDTO struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Project  string `json:"project"`
	Versions []int  `json:"versions"`
	Latest   int    `json:"latest"`
}

type replyDTO struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	Body   string `json:"body"`
}

type commentDTO struct {
	ID        string     `json:"id"`
	LineStart int        `json:"line_start"`
	LineEnd   int        `json:"line_end"`
	WholeFile bool       `json:"whole_file"`
	Quote     string     `json:"quote"`
	Body      string     `json:"body"`
	Status    string     `json:"status"`
	Replies   []replyDTO `json:"replies"`
}

type versionViewDTO struct {
	PlanID     string       `json:"plan_id"`
	Title      string       `json:"title"`
	Number     int          `json:"number"`
	Content    string       `json:"content"`
	Versions   []int        `json:"versions"`
	Latest     int          `json:"latest"`
	Comments   []commentDTO `json:"comments"`
	Carryover  []commentDTO `json:"carryover"`
	PrevNumber int          `json:"prev_number"`
}

func toCommentDTO(c store.Comment) commentDTO {
	replies := make([]replyDTO, 0, len(c.Replies))
	for _, r := range c.Replies {
		replies = append(replies, replyDTO{ID: r.ID, Author: r.Author, Body: r.Body})
	}
	return commentDTO{
		ID:        c.ID,
		LineStart: c.LineStart,
		LineEnd:   c.LineEnd,
		WholeFile: c.WholeFile(),
		Quote:     c.Quote,
		Body:      c.Body,
		Status:    c.Status,
		Replies:   replies,
	}
}

// toCommentDTOs builds comment DTOs from store comments (each carrying its own
// reply thread in Comment.Replies).
func toCommentDTOs(cs []store.Comment) []commentDTO {
	out := make([]commentDTO, 0, len(cs))
	for _, c := range cs {
		out = append(out, toCommentDTO(c))
	}
	return out
}

// ---- Plan endpoints ----

func (h *handlers) apiListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.st.ListPlans()
	if err != nil {
		writeServerError(w, err)
		return
	}
	out := make([]planSummaryDTO, 0, len(plans))
	for _, p := range plans {
		out = append(out, planSummaryDTO{p.ID, p.Title, p.Status, p.Project, p.LatestVersion, p.OpenComments})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) apiCreatePlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Project string `json:"project"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Title == "" {
		writeJSONError(w, http.StatusBadRequest, "title is required")
		return
	}
	if in.Project == "" {
		in.Project = store.NoProject
	}
	p, v, err := h.st.CreatePlan(in.Title, in.Content, in.Project)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"plan_id": p.ID, "number": v.Number})
}

func (h *handlers) apiPlanMeta(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	plan, err := h.st.GetPlan(id)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	latest := 0
	if len(plan.Versions) > 0 {
		latest = plan.Versions[len(plan.Versions)-1]
	}
	writeJSON(w, http.StatusOK, planMetaDTO{plan.ID, plan.Title, plan.Status, plan.Project, plan.Versions, latest})
}

func (h *handlers) apiAddVersion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Content string `json:"content"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	v, err := h.st.AddVersion(id, in.Content)
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
	id := r.PathValue("id")
	plan, err := h.st.GetPlan(id)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	n, err := h.resolveVersionNumber(plan, r.PathValue("n"))
	if err != nil {
		writeVersionErr(w, err)
		return
	}
	version, err := h.st.GetVersion(id, n)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	comments, err := h.st.ListComments(version.ID, false)
	if err != nil {
		writeServerError(w, err)
		return
	}

	latest := plan.Versions[len(plan.Versions)-1]
	var carryover []store.Comment
	prevNumber := n - 1
	if n == latest && prevNumber >= 1 {
		if prev, err := h.st.GetVersion(id, prevNumber); err == nil {
			if carryover, err = h.st.ListComments(prev.ID, true); err != nil {
				writeServerError(w, err)
				return
			}
		}
	}

	writeJSON(w, http.StatusOK, versionViewDTO{
		PlanID:     plan.ID,
		Title:      plan.Title,
		Number:     version.Number,
		Content:    version.Content,
		Versions:   plan.Versions,
		Latest:     latest,
		Comments:   toCommentDTOs(comments),
		Carryover:  toCommentDTOs(carryover),
		PrevNumber: prevNumber,
	})
}

// ---- Comment endpoints ----

func (h *handlers) apiAddComment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	plan, err := h.st.GetPlan(id)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	n, err := h.resolveVersionNumber(plan, r.PathValue("n"))
	if err != nil {
		writeVersionErr(w, err)
		return
	}
	version, err := h.st.GetVersion(id, n)
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
	c, err := h.st.AddComment(version.ID, in.LineStart, in.LineEnd, in.Quote, in.Body)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCommentDTO(c))
}

func (h *handlers) apiResolveComment(w http.ResponseWriter, r *http.Request) {
	h.setStatus(w, r, store.StatusResolved)
}

func (h *handlers) apiReopenComment(w http.ResponseWriter, r *http.Request) {
	h.setStatus(w, r, store.StatusOpen)
}

func (h *handlers) setStatus(w http.ResponseWriter, r *http.Request, status string) {
	cid := r.PathValue("id")
	if err := h.st.SetCommentStatus(cid, status); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) apiKeepComment(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("id")
	if err := h.st.CarryComment(cid); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) apiDeleteComment(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("id")
	if err := h.st.DeleteComment(cid); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) apiAddReply(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("id")
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
	rep, err := h.st.AddReply(cid, in.Author, in.Body)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, replyDTO{ID: rep.ID, Author: rep.Author, Body: rep.Body})
}

func (h *handlers) apiCompletePlan(w http.ResponseWriter, r *http.Request) {
	h.setPlanStatus(w, r, store.PlanCompleted)
}

func (h *handlers) apiReopenPlan(w http.ResponseWriter, r *http.Request) {
	h.setPlanStatus(w, r, store.PlanActive)
}

func (h *handlers) setPlanStatus(w http.ResponseWriter, r *http.Request, status string) {
	id := r.PathValue("id")
	if err := h.st.SetPlanStatus(id, status); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Plan deletion (human-only; not exposed to the agent CLI) ----

func (h *handlers) apiDeletePlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.st.DeletePlan(id); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
