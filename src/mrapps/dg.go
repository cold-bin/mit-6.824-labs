// @author cold bin
// @date 2023/8/9

package main

import (
	"6.5840/mr"
	"fmt"
	"log"
	"strings"
)

func Map(filename string, contents string) []mr.KeyValue {
	// 找出含有`good`单词的每一行
	builder := strings.Builder{}
	ans := make([]mr.KeyValue, 0, 1000)
	line := 0
	for _, r := range []rune(contents) {
		if r == '\n' || r == '\r' {
			line++
			if strings.Contains(contents, "good") {
				ans = append(ans, mr.KeyValue{Key: fmt.Sprint(line), Value: builder.String()})
				log.Printf("%d-------%s", line, builder.String())
			}
			builder.Reset()
		} else {
			builder.WriteRune(r)
		}
	}

	return ans
}

func Reduce(key string, values []string) string {
	return fmt.Sprint(key, "-------", values, "\n")
}
