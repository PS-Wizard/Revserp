package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestUserQuestionAnswersEqual(t *testing.T) {
	if !userQuestionAnswersEqual(nil, nil) {
		t.Error("nil/nil must be equal")
	}
	if !userQuestionAnswersEqual([]byte("null"), nil) {
		t.Error("null/nil must be equal")
	}
	if userQuestionAnswersEqual([]byte(`[{"a":1}]`), nil) {
		t.Error("answers/nil must differ")
	}
	if !userQuestionAnswersEqual(
		[]byte(`[{"question_id":"q1","selected_option_ids":["a"]}]`),
		[]byte(`[ { "selected_option_ids" : [ "a" ] , "question_id" : "q1" } ]`)) {
		t.Error("canonically identical answers read as different")
	}
	if userQuestionAnswersEqual(
		[]byte(`[{"question_id":"q1","selected_option_ids":["a"]}]`),
		[]byte(`[{"question_id":"q1","selected_option_ids":["b"]}]`)) {
		t.Error("different selections read as identical")
	}
	if userQuestionAnswersEqual([]byte(`[`), []byte(`[`)) {
		t.Error("invalid JSON must never compare equal")
	}
}

func TestUserQuestionRequestJSONShape(t *testing.T) {
	row := sqlc.ListUserQuestionRequestsForConversationRow{
		Status:    "pending",
		Questions: []byte(`[{"id":"q1","header":"Scope","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]`),
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	entity, err := userQuestionRequestJSON(row)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entity["question_request"]; ok {
		t.Fatalf("entity must be bare (callers wrap it): %v", entity)
	}
	if entity["status"] != "pending" {
		t.Fatalf("entity = %v", entity)
	}
	var questions []any
	raw, _ := json.Marshal(entity["questions"])
	if err := json.Unmarshal(raw, &questions); err != nil || len(questions) != 1 {
		t.Fatalf("questions = %v", entity["questions"])
	}
}
