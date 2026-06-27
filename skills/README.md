# Skills — for AI agents using gosqlite

These are [Agent Skills](https://docs.claude.com/en/docs/agents-and-tools/agent-skills/overview): task-scoped instructions that teach an AI agent how to **use** `gosqlite.org` (and [LiteORM](https://liteorm.org), an ORM built on it) correctly when building an application with it. Each skill is a folder with a `SKILL.md` whose frontmatter `description` says when to load it; the agent reads the description, then pulls in the body when the task matches.

| Skill | Load it when the task is… |
|---|---|
| [`using-gosqlite`](using-gosqlite/SKILL.md) | opening a database / picking a driver name / choosing a sub-package (start here) |
| [`migrating`](migrating/SKILL.md) | switching from mattn / modernc / glebarez / gorm.io/driver/sqlite / ncruces |
| [`vector-search`](vector-search/SKILL.md) | semantic / similarity / KNN search |
| [`full-text-search`](full-text-search/SKILL.md) | keyword / FTS5 / BM25 search |
| [`hybrid-search`](hybrid-search/SKILL.md) | combining vector + keyword results |
| [`liteorm`](liteorm/SKILL.md) | an ORM with native vector / full-text / hybrid search, built on gosqlite |
| [`gorm`](gorm/SKILL.md) | using `gorm.io/gorm` with the gosqlite dialector |
| [`encryption-and-vfs`](encryption-and-vfs/SKILL.md) | encryption, compression at rest, checksums, in-memory, or `embed.FS` databases |
| [`custom-vfs`](custom-vfs/SKILL.md) | backing a DB with custom Go storage |
| [`extensions`](extensions/SKILL.md) | needing a SQL function/vtab (regexp, uuid, hash, csv, …) |
| [`blob-storage`](blob-storage/SKILL.md) | storing large / growable / streamed byte objects (files, uploads) in SQLite |
| [`pitfalls`](pitfalls/SKILL.md) | debugging surprising behaviour, or before shipping |

## For maintainers

Skills ship to consumers and **go stale silently**. When a feature changes, update the matching skill in the same change — this is part of [`AGENTS.md`](../AGENTS.md)'s doc-update checklist. The human-facing equivalents are [`docs/`](../docs/) (narrative guides) and [pkg.go.dev](https://pkg.go.dev/gosqlite.org) (API reference).

To make these discoverable to a local Claude Code session, symlink them: `ln -s ../skills .claude/skills` (or copy the ones you want).
