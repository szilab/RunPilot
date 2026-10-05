package main

//go:generate tinygo build -target=wasm-unknown -tags=runpilot_wasm -scheduler=none -gc=conservative -stack-size=64kb -no-debug -o plugin.wasm .
