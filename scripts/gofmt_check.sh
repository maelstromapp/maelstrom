#!/bin/bash -e

out=$(gofmt -l cmd pkg)
if [ -n "$out" ]; then
    echo "ERROR: Some files require gofmt formatting:"
    echo $out
    echo
    echo "Failing build"
    exit 1
fi
