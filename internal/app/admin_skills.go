package app

import (
	"net/http"
)

// adminSkillInfo is one skill in the admin listing: metadata and skill-local
// file paths only, never file bodies or host paths.
type adminSkillInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Files       []string `json:"files"`
}

// adminSkillsResponse is the GET /admin/skills response. Skills is never
// null and is sorted by id.
type adminSkillsResponse struct {
	Skills []adminSkillInfo `json:"skills"`
}

// handleAdminListSkills returns the Git-managed skill catalog metadata for
// platform admins. A malformed committed skill fails with its diagnostic so
// a bad checkout surfaces instead of serving a silent partial list.
func (a *App) handleAdminListSkills(w http.ResponseWriter, r *http.Request) {
	_ = r
	skills := []adminSkillInfo{}
	if a.Skills != nil {
		if err := a.Skills.Err(); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, skill := range a.Skills.Skills() {
			files := make([]string, len(skill.Files))
			copy(files, skill.Files)
			skills = append(skills, adminSkillInfo{ID: skill.ID, Name: skill.Name, Description: skill.Description, Files: files})
		}
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, adminSkillsResponse{Skills: skills})
}
