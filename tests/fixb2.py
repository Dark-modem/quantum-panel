p = "tests/ui_smoke_container_test.sh"
s = open(p, encoding="utf-8").read()
# Drop the fragile wsl fallback: the go build happens on the host (Git Bash
# go) or via a pre-built AGENT_BIN-style env PANEL_BIN; keep it simple.
import re
start = s.index("else\n    # WSL fallback")
end = s.index("fi", s.index("cmd/quantum-panel")) + len("fi")
s = s[:start] + "else\n    echo \"FAIL: no local go — build the panel binary first (see tests/ui_smoke.py)\"\n    exit 1\n" + s[end:]
# If a prebuilt binary is already in .ui-work, copy is unnecessary but harmless.
open(p, "w", encoding="utf-8", newline="\n").write(s)
print("fallback removed")
