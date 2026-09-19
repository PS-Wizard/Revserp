package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ps-wizard/revserp/internal/googlesuggest"
)

const (
	getSearchSuggestionsName            = "get_search_suggestions"
	getSearchSuggestionsMaxSeedLength   = 120
	getSearchSuggestionsMaxPhraseLength = 120
	getSearchSuggestionsMaxShown        = 60
	// Expand is the bare seed plus one request per letter, so 27.
	getSearchSuggestionsExpandCost   = 27
	getSearchSuggestionsLimitMessage = "Search suggestion limit reached for this turn."
	getSearchSuggestionsUnavailable  = "Search suggestions are unavailable right now."
	getSearchSuggestionsNotAvailable = "Search suggestions are not available for this project."
)

const getSearchSuggestionsSchema = `{
  "type": "object",
  "properties": {
    "seed": {"type": "string", "maxLength": 120, "description": "A short head term to complete, for example \"seo audit\". Use 1 to 4 words, never a full sentence."},
    "expand": {"type": "boolean", "description": "Also collect completions for every letter appended to the seed. Costs about 27 upstream requests and returns many more phrases. Default false."},
    "language": {"type": "string", "maxLength": 10, "description": "Optional language hint such as \"en\"."},
    "country": {"type": "string", "maxLength": 10, "description": "Optional country hint such as \"us\". Google treats this as a hint, not a filter."}
  },
  "required": ["seed"],
  "additionalProperties": false
}`

const getSearchSuggestionsDescription = "List the completions Google autocomplete shows for a short seed phrase. Use it to ground keyword and question work in phrases people actually type: expanding target and non-branded keywords, or finding realistic ways a category is searched. It returns completions only. It has no search volume, no difficulty, and no ranking, so never present a phrase count as demand and never claim a phrase is popular. Set expand to true only when you need breadth for one head term, because it costs about 27 upstream requests and a turn allows about 40. Seed with 1 to 4 words, not a sentence: a long seed completes the popular prefix instead of your tail. Pass language and country when the project market is clear, and treat them as hints because Google ignores them for some queries. Never put a phrase that contains the brand name into non_branded_keywords. The tool is unavailable when the worker cannot reach the endpoint."

// SuggestClient is the autocomplete path the worker provides. It is a small
// interface so tool tests can substitute fakes. The worker leaves Scope.Suggest
// nil when the worker cannot reach the endpoint; the tool reports that as an
// ordinary unavailable state, not an error.
type SuggestClient interface {
	Suggest(ctx context.Context, seed string, opts googlesuggest.Options) ([]googlesuggest.Suggestion, error)
	Expand(ctx context.Context, seed, letters string, opts googlesuggest.Options, maxRequests int) ([]googlesuggest.Suggestion, error)
}

// SuggestBudget is a thread-safe count of the suggestion calls and upstream
// requests one turn may still spend. A nil budget means no cap (raw tool-call
// mode), matching the other budgets. Calls and requests are capped separately:
// the call cap stops a model looping the tool, and the request cap stops an
// expand-heavy turn from fanning out dozens of requests.
type SuggestBudget struct {
	mu           sync.Mutex
	callsLeft    int
	requestsLeft int
}

// ErrSuggestBudgetExhausted reports that the turn spent its suggestion allowance.
var ErrSuggestBudgetExhausted = errors.New("suggestion budget exhausted for this turn")

// NewSuggestBudget returns a budget with the given call and request
// allowances. Negative limits become zero.
func NewSuggestBudget(calls, requests int) *SuggestBudget {
	if calls < 0 {
		calls = 0
	}
	if requests < 0 {
		requests = 0
	}
	return &SuggestBudget{callsLeft: calls, requestsLeft: requests}
}

// SpendCall reserves one call. A nil budget allows the call.
func (b *SuggestBudget) SpendCall() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.callsLeft <= 0 {
		return ErrSuggestBudgetExhausted
	}
	b.callsLeft--
	return nil
}

// SpendRequests reserves n upstream requests, all or nothing. A nil budget
// allows the call.
func (b *SuggestBudget) SpendRequests(n int) error {
	if b == nil {
		return nil
	}
	if n < 0 {
		n = 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.requestsLeft < n {
		return ErrSuggestBudgetExhausted
	}
	b.requestsLeft -= n
	return nil
}

func getSearchSuggestionsTool() Tool {
	return Tool{
		Def: Def{
			Name:        getSearchSuggestionsName,
			Label:       "Search suggestions",
			Feature:     "ai_chat",
			Description: getSearchSuggestionsDescription,
			Schema:      json.RawMessage(getSearchSuggestionsSchema),
		},
		Execute: executeGetSearchSuggestions,
	}
}

// getSearchSuggestionsExecutor runs one get_search_suggestions call. The client
// and budget come from the request scope so tests can substitute fakes.
type getSearchSuggestionsExecutor struct {
	suggest SuggestClient
	budget  *SuggestBudget
}

// executeGetSearchSuggestions adapts the tool contract to the narrow executor. A
// missing client is an ordinary unavailable state, not an error; only broken
// input such as a missing seed is returned as an error.
func executeGetSearchSuggestions(ctx context.Context, raw json.RawMessage, s Scope) (Result, error) {
	if s.Suggest == nil {
		return Result{Content: getSearchSuggestionsNotAvailable, Summary: "search suggestions unavailable"}, nil
	}
	exec := getSearchSuggestionsExecutor{suggest: s.Suggest, budget: s.SuggestBudget}
	return exec.run(ctx, raw)
}

type getSearchSuggestionsArgs struct {
	seed     string
	expand   bool
	language string
	country  string
}

func (e *getSearchSuggestionsExecutor) run(ctx context.Context, raw json.RawMessage) (Result, error) {
	args, err := parseGetSearchSuggestionsArgs(raw)
	if err != nil {
		return Result{}, err
	}

	cost := 1
	if args.expand {
		cost = getSearchSuggestionsExpandCost
	}

	// Spend the turn allowance before touching the network so a rate-limit
	// rejection never reaches the endpoint. Calls and requests are spent
	// separately, and an expand is all-or-nothing: a request allowance that
	// cannot cover the whole cost is refused rather than silently truncated.
	// A nil budget means no cap.
	if err := e.spend(cost); err != nil {
		if errors.Is(err, ErrSuggestBudgetExhausted) {
			return Result{Content: getSearchSuggestionsLimitMessage, Summary: "search suggestion limit reached"}, nil
		}
		return Result{}, fmt.Errorf("%s: spend budget: %w", getSearchSuggestionsName, err)
	}

	opts := googlesuggest.Options{Language: args.language, Country: args.country}
	var suggestions []googlesuggest.Suggestion
	if args.expand {
		suggestions, err = e.suggest.Expand(ctx, args.seed, "", opts, cost)
	} else {
		suggestions, err = e.suggest.Suggest(ctx, args.seed, opts)
	}
	if err != nil {
		// The underlying error may carry endpoint internals; keep it out of the
		// model-facing content and report a plain unavailable state.
		return Result{Content: getSearchSuggestionsUnavailable, Summary: "search suggestions unavailable"}, nil
	}
	if len(suggestions) == 0 {
		return Result{Content: fmt.Sprintf("No suggestions for %q.", args.seed), Summary: "no suggestions"}, nil
	}
	return formatSearchSuggestions(args.seed, suggestions), nil
}

// spend reserves one call, then its request cost. A request allowance that
// cannot cover the whole cost is refused and left untouched.
func (e *getSearchSuggestionsExecutor) spend(cost int) error {
	if err := e.budget.SpendCall(); err != nil {
		return err
	}
	return e.budget.SpendRequests(cost)
}

// parseGetSearchSuggestionsArgs parses the tool arguments strictly. Empty input
// is a missing seed.
func parseGetSearchSuggestionsArgs(raw json.RawMessage) (getSearchSuggestionsArgs, error) {
	var args getSearchSuggestionsArgs
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for key, value := range fields {
		switch key {
		case "seed":
			if err := json.Unmarshal(value, &args.seed); err != nil {
				return args, errors.New("argument \"seed\" must be a string")
			}
		case "expand":
			if err := json.Unmarshal(value, &args.expand); err != nil {
				return args, errors.New("argument \"expand\" must be a boolean")
			}
		case "language":
			if err := json.Unmarshal(value, &args.language); err != nil {
				return args, errors.New("argument \"language\" must be a string")
			}
		case "country":
			if err := json.Unmarshal(value, &args.country); err != nil {
				return args, errors.New("argument \"country\" must be a string")
			}
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	args.seed = strings.TrimSpace(args.seed)
	if args.seed == "" {
		return args, errors.New("argument \"seed\" is required")
	}
	if utf8.RuneCountInString(args.seed) > getSearchSuggestionsMaxSeedLength {
		return args, fmt.Errorf("argument \"seed\" must be at most %d characters", getSearchSuggestionsMaxSeedLength)
	}
	return args, nil
}

// formatSearchSuggestions renders the completions as a numbered plain-text list,
// deduping case-insensitively and capping how many phrases reach the model.
func formatSearchSuggestions(seed string, suggestions []googlesuggest.Suggestion) Result {
	seen := make(map[string]struct{}, len(suggestions))
	unique := make([]string, 0, len(suggestions))
	for _, suggestion := range suggestions {
		key := strings.ToLower(suggestion.Phrase)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, suggestion.Phrase)
	}

	shown := unique
	omitted := 0
	if len(shown) > getSearchSuggestionsMaxShown {
		omitted = len(shown) - getSearchSuggestionsMaxShown
		shown = shown[:getSearchSuggestionsMaxShown]
	}

	var b strings.Builder
	for i, phrase := range shown {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d. %s", i+1, truncateSuggestionPhrase(phrase))
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\n%d more suggestions omitted.", omitted)
	}

	summary := fmt.Sprintf("Suggestions for %q · %d phrases", seed, len(unique))
	return Result{Content: b.String(), Summary: summary}
}

// truncateSuggestionPhrase caps a single phrase so one completion cannot
// dominate the output, marking the cut with an ellipsis.
func truncateSuggestionPhrase(phrase string) string {
	if utf8.RuneCountInString(phrase) <= getSearchSuggestionsMaxPhraseLength {
		return phrase
	}
	return string([]rune(phrase)[:getSearchSuggestionsMaxPhraseLength]) + "\u2026"
}
