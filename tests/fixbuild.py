p = "tests/ui_smoke_container_test.sh"
s = open(p, encoding="utf-8").read()
old = '''else
    wsl.exe -d Ubuntu-24.04 -- bash -lc \
        "export GOFLAGS=-mod=mod GOPROXY=file:///tmp/goproxy,https://proxy.golang.org GOSUMDB=off GOPRIVATE=github.com/Dark-modem/* GOPATH=\$HOME/go; cd /mnt/c/Users/-Mahdi/Downloads/Quantum/01-REPOSITORIES/wt-phase4-ui && CGO_ENABLED=0 go build -trimpath -o '$(cygpath -m "$WORK" | sed 's|^C:|/mnt/c|')/quantum-panel' ./cmd/quantum-panel"
fi'''
new = '''else
    # WSL fallback: the worktree has a fixed /mnt/c path in this workspace.
    wsl.exe -d Ubuntu-24.04 -- bash -lc "export GOFLAGS=-mod=mod GOPROXY=file:///tmp/goproxy,https://proxy.golang.org GOSUMDB=off GOPRIVATE=github.com/Dark-modem/* GOPATH=\$HOME/go; cd /mnt/c/Users/-Mahdi/Downloads/Quantum/01-REPOSITORIES/wt-phase4-ui && CGO_ENABLED=0 go build -trimpath -o /mnt/c/Users/-Mahdi/Downloads/Quantum/01-REPOSITORIES/wt-phase4-ui/tests/.ui-work/quantum-panel ./cmd/quantum-panel"
fi'''
assert old in s, "build fallback anchor missing"
s = s.replace(old, new)
open(p, "w", encoding="utf-8", newline="\n").write(s)
print("ok")
