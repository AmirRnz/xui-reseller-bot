# Project-Scoped Agent Rules

## Git Workflow
- **Do NOT commit anything to git.** The agent must not run `git commit` or `git push`.
- The user will handle committing and pushing changes manually.

## Database Migrations
- Whenever a database migration or schema change is required, you **MUST ask the user** whether the database contains production data.
- If there is **no production data**, you can safely wipe the database and create a fresh one.
- If there **is production data**, you must proceed carefully and write/execute a proper migration to preserve the existing data.

## Context & Documentation
- For anything related to the 3x-ui API, consult the `xui-openai-docs.json` file in the repository root.
- For other information or project context, scan the local repository files.

## External MCP Tools Usage
Be proactive and explicit in using the available MCP servers:
- **`github`**: Use this to search GitHub repositories, read open-source code, or review issues and PRs when you need external reference material.
- **`firecrawl`**: Use this for web scraping, searching the internet, or extracting documentation from websites when dealing with unknown errors or learning about a library.
- **`context7`**: Use this to query up-to-date documentation for third-party libraries and resolve library IDs.

## Deployment & Naming
- The project is named `xui-reseller-bot`. All Go files and module name use the module path `xui-reseller-bot`.
- The bot is deployed to the 203.202.232.117 VPS using the `turk1-mcp-server` MCP server.
- The remote deployment directory on the VPS is `/opt/xui-reseller-bot`.
- The systemd service on the VPS is `xui-reseller-bot.service`.

