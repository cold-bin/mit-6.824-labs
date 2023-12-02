#!/usr/bin/env bash

cnt=$2
if [[ !($cnt =~ ^[0-9]+$) ]]; then
    echo "$cnt 不是数字"
    echo "usage: test.sh which_item test_times"
    exit
fi

function tests() {
    num=$1
    while [ $num -gt 0 ]; do
        VERBOSE=1 go test -race -run 2$2 >> test2A_$3_times
        let "num--"
    done
}

num=$cnt

case $1 in
  A)
    echo "Test lab2A ${cnt} times..." >> test2A_${cnt}_times
    tests $cnt 'A' $cnt
    ;;
  B)
    echo "Test lab2B ${cnt} times..." >> test2B_${cnt}_times
    tests $cnt 'B' $cnt
    ;;
  C)
    echo "Test lab2C ${cnt} times..." >> test2C_${cnt}_times
    tests $cnt 'C' $cnt
    ;;
  *)
    echo "invalid $1"
    echo "usage: test.sh which_item test_times"
    ;;
esac