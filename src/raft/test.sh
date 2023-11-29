#!/usr/bin/zsh
for i in {1..100} ; do
  VERBOSE=1 go test -race -run 2B >> test_times_100
done