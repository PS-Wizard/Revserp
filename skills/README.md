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

## Admin and deployment

The admin Skills tab lists the deployed catalog and its file paths. It does not edit files or show file bodies.

`AI_SKILLS_DIR` defaults to `skills`, relative to the process working directory. Run local API and chat worker commands from the backend root, or set that variable to the directory's absolute path. Both backend runtime images copy this directory to `/app/skills`.

Run the skill checks before deploying:

```sh
go test ./internal/aiskills ./internal/aichattools ./internal/app ./internal/aichatworker
```
