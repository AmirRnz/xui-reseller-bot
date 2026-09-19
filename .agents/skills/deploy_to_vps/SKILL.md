---
name: deploy_to_vps
description: Deploy the latest pushed changes to the VPS by pulling the git repository, recompiling, and restarting the bot service.
---

# Deploy to VPS

When you are asked to use this skill, perform the deployment immediately using the `turk1-mcp-server` server's `execute-command` tool. You can assume the user has already committed and pushed their changes.

Based on previous deployments, here are the correct paths and commands you must use:
1. **Directory**: The bot is located at `/opt/xui-reseller-bot`.
2. **Pull Changes**: Run `git pull` from that directory.
3. **Recompile**: The main package is in `./cmd/bot`, so you must build it with `/usr/bin/go build -o bot_linux ./cmd/bot`. Do not build `./cmd` directly.
4. **Restart Service**: The systemd service is named `xui-reseller-bot.service`. Run `systemctl restart xui-reseller-bot.service`.
5. **Verify Status**: Check the health of the bot using `systemctl status xui-reseller-bot.service --no-pager`.

**Complete One-Liner Command:**
```bash
cd /opt/xui-reseller-bot && git pull && /usr/bin/go build -o bot_linux ./cmd/bot && systemctl restart xui-reseller-bot.service && sleep 2 && systemctl status xui-reseller-bot.service --no-pager
```

