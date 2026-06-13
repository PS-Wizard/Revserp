# Global controls

## Coverage scale

### Intuition
Controls **how fast issue impact ramps up as it affects more URLs**.

A higher coverage scale means:
- the score reacts more aggressively to issues spreading across the site
- even moderate spread starts to matter quickly

A lower coverage scale means:
- issues need to affect a larger share of the crawl before they really bite
- the system is more forgiving to partial coverage

### Think of it like
- **high coverage scale** = “sitewide spread matters a lot, quickly”
- **low coverage scale** = “don’t overreact unless the issue is widespread”

### Example
If `missing_title` affects:
- 2 URLs out of 500 → small coverage effect
- 200 URLs out of 500 → large coverage effect

Coverage scale controls **how steeply** you move between those two cases.

---

## Volume pressure

### Intuition
Controls how much extra penalty gets added when the crawl has **lots of issue rows per page overall**.

This is not about one issue type.
It is about **total issue density**.

A higher volume pressure means:
- cluttered / noisy crawls get punished more
- lots of smaller issues can compound harder

A lower volume pressure means:
- total issue volume matters less
- scoring stays closer to the raw summed penalties

### Think of it like
- **high volume pressure** = “too many issues everywhere is itself a problem”
- **low volume pressure** = “judge mostly by issue severity, not total clutter”

---

## Max volume pressure

### Intuition
Puts a ceiling on how much the volume pressure system can amplify penalties.

Without this, very noisy crawls could get hit too hard.

A higher max volume pressure means:
- issue-density amplification is allowed to grow more

A lower max volume pressure means:
- even very messy crawls stop getting extra punishment after a point

### Think of it like
- **volume pressure** = how hard the extra pressure ramps
- **max volume pressure** = how far that ramp is allowed to go

---

# Severity multipliers

Severity multipliers scale the **base penalty** for each issue row.

## High
Usually the strongest multiplier.

### Intuition
Critical or serious issues should preserve most of their base penalty.

If high = `1.0`
- a base penalty of `12` stays `12`

---

## Medium
Moderate issues get reduced relative to high.

### Intuition
Still important, but not as damaging as truly critical issues.

If medium = `0.6`
- a base penalty of `12` lands like `7.2`

---

## Low
Minor issues get reduced the most.

### Intuition
Useful to surface, but should not dominate the score.

If low = `0.3`
- a base penalty of `12` lands like `3.6`

---

## How to think about severity multipliers overall

They answer:

> “If two issue types have the same base penalty, how much should severity reduce or preserve that impact?”

If you widen the gap between high/medium/low:
- severity matters more

If you compress the gap:
- severity matters less
- base penalties matter more directly

---

# Overall weights

These control how much each top-level pillar contributes to the **final overall score**.

- SEO
- AEO
- PageSpeed

## SEO overall weight

### Intuition
How much classic SEO should matter in the total product score.

Increase it if:
- search fundamentals should dominate the overall grade

Decrease it if:
- you want a more balanced or broader quality model

---

## AEO overall weight

### Intuition
How much answerability / trust / structured-data style quality should matter in the final score.

Increase it if:
- you want brand/entity/trust/schema quality to matter more

Decrease it if:
- you want AEO to stay secondary to classic SEO

---

## PageSpeed overall weight

### Intuition
How much performance quality should influence the final score.

Increase it if:
- performance is strategically important
- speed issues should visibly drag the whole crawl score down

Decrease it if:
- performance should be visible but not dominate the total

---

## Important note on overall weights

These weights affect the **overall score only**.

They do **not** change:
- which issues exist
- the internal score math inside each pillar

They only change:
- how much each finished pillar score contributes to the final total

---

# Pillars

A **pillar** is a major scoring category.

Current pillars are:
- SEO
- AEO
- PageSpeed

Each pillar has:
- its own score
- its own buckets
- its own issue type penalties

---

# Bucket weights

Inside each pillar, issues are grouped into **buckets**.

Examples:
- SEO might have buckets like metadata, structure, indexability, internal linking
- PageSpeed might have responsiveness / page weight
- AEO might have trust / expertise / answerability

## What a bucket weight does

It controls how much that bucket contributes to the **pillar score**.

A higher bucket weight means:
- that bucket has more influence inside the pillar

A lower bucket weight means:
- that bucket matters less relative to other buckets in the same pillar

### Think of it like
Inside a pillar, bucket weights answer:

> “Which kinds of problems matter more within this pillar?”

---

## Example
In SEO:
- if `indexability` has a high weight
- and `media_optimization` has a lower weight

Then:
- canonical / noindex / status issues pull SEO down more
- image alt/dimension issues pull SEO down less

Even if both buckets contain issues.

---

# Issue penalties

Each issue type has a **base penalty**.

Examples:
- `missing_title`
- `thin_content`
- `missing_structured_data`
- `slow_response_time`

## What base penalty means

This is the raw importance of that issue **before** severity and coverage math are applied.

A higher base penalty means:
- this issue type is fundamentally more serious in the model

A lower base penalty means:
- this issue type is still valid, but should have less scoring force

### Think of it like
Base penalty answers:

> “How bad is this issue type in principle?”

---

## Example
If:
- `missing_title = 12`
- `images_missing_dimensions = 4`

Then the system is saying:
- missing titles are much more important than missing image dimensions

before severity and coverage are even considered.

---

# How a single issue becomes a final penalty

Roughly:

`final issue penalty = base penalty × severity multiplier × coverage effect`

Then bucket-level and pillar-level weighting happens after that.

And then volume pressure can further amplify the total pillar penalty.

---

# Practical tuning intuition

## If scores feel too harsh overall
Try lowering:
- issue base penalties
- coverage scale
- volume pressure
- max volume pressure

## If scores feel too forgiving
Try raising:
- issue base penalties
- coverage scale
- volume pressure

## If minor issues are dragging scores too much
Lower:
- low severity multiplier
- medium severity multiplier

## If critical issues are not hitting hard enough
Raise:
- high severity multiplier
- specific critical issue penalties
- bucket weights around those issues

## If one pillar should matter more strategically
Raise its:
- overall weight

## If one type of problem inside a pillar should matter more
Raise its:
- bucket weight
- or issue penalty

---

# What these controls do **not** change

These controls do **not** decide whether an issue gets flagged.

They do not change derivation rules like:
- title too long threshold
- thin content threshold
- duplicate similarity threshold
- low internal links threshold
- very deep page threshold

Those are **detection / derivation rules**, not scoring rules.

---

# Fast cheat sheet

## Coverage scale
How quickly an issue becomes serious as it affects more URLs.

## Volume pressure
How much extra punishment comes from having lots of issue rows overall.

## Max volume pressure
The cap on that extra issue-density punishment.

## Severity multipliers
How much high / medium / low severity scales the base penalty.

## Overall weights
How much SEO / AEO / PageSpeed affect the final overall score.

## Bucket weights
How much each bucket matters inside its pillar.

## Issue penalties
How serious each issue type is before severity/coverage are applied.

---

# Best mental model

Use the controls in this order:

1. **Issue penalty**
   - “How bad is this issue type?”
2. **Severity multiplier**
   - “How much should severity matter?”
3. **Coverage scale**
   - “How much more serious is this when it spreads?”
4. **Volume pressure**
   - “How much should issue density amplify total penalties?”
5. **Bucket weight**
   - “Which problem families matter more within a pillar?”
6. **Overall weight**
   - “Which pillars matter more in the final score?”

That is the cleanest way to reason about the system.
