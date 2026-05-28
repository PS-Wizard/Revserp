package app

import (
	"encoding/json"
	"net/http"
)

// readJSON decodes a JSON request body.
func readJSON(r *http.Request, target any) error {
	return json.NewDecoder(r.Body).Decode(target)
}
