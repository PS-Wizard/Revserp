package seo

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// referenceCharacterTrigrams is the pre-fix implementation, kept byte-for-byte
// except the removed capacity hint, to prove the fix changes nothing.
func referenceCharacterTrigrams(value string) map[string]struct{} {
	if value == "" {
		return nil
	}
	if len(value) < 3 {
		return map[string]struct{}{value: {}}
	}
	characterTrigrams := make(map[string]struct{}, len(value)-2)
	for trigramStartIndex := 0; trigramStartIndex <= len(value)-3; trigramStartIndex++ {
		characterTrigrams[value[trigramStartIndex:trigramStartIndex+3]] = struct{}{}
	}
	return characterTrigrams
}

func TestBuildCharacterTrigramsEquivalence(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"empty":            "",
		"one byte":         "a",
		"two bytes":        "ab",
		"three bytes":      "abc",
		"repetitive":       "aaaaa",
		"overlapping":      "ababab",
		"ascii words":      "the quick brown fox jumps",
		"multibyte runes":  "héllo wörld 日本語テスト",
		"mixed bytes":      "aéあb",
		"emoji":            "😀😃😄😁",
		"whitespace":       "  \t\n ",
		"repetitive 64KiB": strings.Repeat("abcd ", (64<<10)/5),
		"repetitive 1MiB":  strings.Repeat("lorem ipsum dolor ", (1<<20)/18),
	}
	for name, value := range cases {
		got, err := buildCharacterTrigrams(context.Background(), value)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		want := referenceCharacterTrigrams(value)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: trigram set differs (got %d, want %d entries)", name, len(got), len(want))
		}
		if (got == nil) != (want == nil) {
			t.Errorf("%s: nil-ness differs", name)
		}
	}
}

func TestBuildCharacterTrigramsCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	trigrams, err := buildCharacterTrigrams(ctx, strings.Repeat("abcd ", 1<<20))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if trigrams != nil {
		t.Fatalf("cancelled build returned %d trigrams, want none", len(trigrams))
	}
}

func benchmarkTrigramInput(size int, distinct bool) string {
	if !distinct {
		return strings.Repeat("abcd ", size/5)
	}
	rng := rand.New(rand.NewSource(42))
	var sb strings.Builder
	sb.Grow(size)
	for sb.Len() < size {
		sb.WriteByte(byte('a' + rng.Intn(26)))
	}
	return sb.String()
}

func BenchmarkBuildCharacterTrigrams(b *testing.B) {
	ctx := context.Background()
	for _, size := range []int{64 << 10, 1 << 20} {
		for _, distinct := range []bool{false, true} {
			kind := "repetitive"
			if distinct {
				kind = "distinct"
			}
			input := benchmarkTrigramInput(size, distinct)
			b.Run(fmt.Sprintf("%s-%dKiB/old-hint", kind, size>>10), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_ = referenceCharacterTrigrams(input)
				}
			})
			b.Run(fmt.Sprintf("%s-%dKiB/no-hint", kind, size>>10), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_, _ = buildCharacterTrigrams(ctx, input)
				}
			})
		}
	}
}
