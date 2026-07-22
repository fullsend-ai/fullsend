module github.com/fullsend-ai/fullsend/cmd/mint-wasm

go 1.26

require github.com/fullsend-ai/fullsend/internal/mintcore v0.0.0

require golang.org/x/sync v0.20.0 // indirect

replace github.com/fullsend-ai/fullsend/internal/mintcore => ../../internal/mintcore
