# Revbot skills

This directory contains Git-managed instructions that Revbot can load through its tools. Commit skill changes and deploy the backend to update the API and chat worker together. There is no admin upload endpoint.

## Add a skill

Create a folder containing `SKILL.md` with YAML frontmatter:

```markdown
---
name: content-review
description: Review website copy for clarity and prepare an evidence-based action plan.
---

# Content review

Describe when to use the skill and its procedure. Link to reference files by their path relative to this folder.
```

Category folders and reference subfolders are supported:

```text
skills/
  seo/
    local-seo/
      SKILL.md
      references/
        checklist.md
```

The skill ID is its directory path relative to `skills/`, such as `seo/local-seo`. Each directory containing `SKILL.md` defines an independent skill. Category folders do not add inherited instructions.

## Loading

`list_skills` returns skill IDs, names, and descriptions. It does not return instructions or reference content. `read_skill` reads one skill-local Markdown or text file. Its path defaults to `SKILL.md`. Reference files load only when Revbot requests them.

Reads are bounded and paged. Follow `next_offset` while `has_more` is true, and pass the returned revision on continuation reads. Read the complete `SKILL.md` before applying its instructions. Keep the root instructions short and put detailed examples in references.

`SKILL.md` is limited to 64 KiB. Other readable files are limited to 256 KiB. A read returns at most 12 KiB of text and can return less to fit the serialized tool result limit. The worker limits total skill text reads to 96 KiB per turn. These limits do not increase the model's context capacity or its tool-round allowance. Keep each procedure small enough to leave room for product data and the answer.

Skill files provide supplemental task guidance. They cannot grant tool permissions, override base rules, or execute scripts. Do not put secrets in this directory. Reads reject paths outside the selected skill.

## Imported skills

`seo-audit`, `ai-seo`, and `content-strategy` are imported from
`coreyhaines31/marketingskills` (MIT, Copyright (c) 2025 Corey Haines; full
text in each skill's `LICENSE.txt`). They complement the in-house
`seo-aeo-best-practices` principles with procedures: SEO diagnosis (`seo-audit`),
AI-answer citation and agent readiness (`ai-seo`), and keyword research by buyer
stage plus content gaps (`content-strategy`, "Keyword Research by Buyer Stage"
and "Content Ideation Sources"). Upstream commit `365b412` (2026-10-15);
skill versions: seo-audit 2.2.1, ai-seo 2.9.0, content-strategy 2.1.3.

Installed with Bun in a temp staging dir (never in this repo), one per skill:

```sh
bunx skills add coreyhaines31/marketingskills --skill <seo-audit|ai-seo|content-strategy> --copy --yes --agent opencode
```

Then only `SKILL.md` and `references/*.md` were copied into `skills/<id>/`.
`evals/`, `skills-lock.json`, and agent-dir links were left behind. Each
`SKILL.md` carries a short Revbot adaptation note after its frontmatter, and
the `../SKILL.md` link in `ai-seo/references/content-types.md` was fixed to
the catalog-relative `SKILL.md`; all other imported content is unmodified.

The in-skill Revbot note points at project tools instead: the optional
`.agents/product-marketing.md` context file does not exist here, references
to sibling skills outside this catalog (schema, programmatic-seo,
copywriting, social, emails, launch) name skills that are not installed, and
external tool pointers (vendor CLIs, tools registry URLs, scoring sites) cannot
run here. Use project tools and catalog skills instead, label unavailable
inputs as missing, and never fabricate data. Dated stats and platform
behavior drift; re-check sources before quoting them.

## Admin and deployment

The admin Skills tab lists the deployed catalog and its file paths. It does not edit files or show file bodies.

`AI_SKILLS_DIR` defaults to `skills`, relative to the process working directory. Run local API and chat worker commands from the backend root, or set that variable to the directory's absolute path. Both backend runtime images copy this directory to `/app/skills`.

Run the skill checks before deploying:

```sh
go test ./internal/aiskills ./internal/aichattools ./internal/app ./internal/aichatworker
```
