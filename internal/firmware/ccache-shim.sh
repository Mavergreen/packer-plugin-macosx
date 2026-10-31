#!/bin/sh
# platform: host-agnostic -- the ccache shim writeCcacheShims (ccache.go) writes as gcc and g++
exec @CCACHE@ @COMPILER@ "$@"
