// coveragecheck 校验 Go cover profile 的语句覆盖率门槛。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	profile := flag.String("profile", "coverage.out", "Go cover profile 路径")
	minimum := flag.Float64("minimum", 90, "最低语句覆盖率百分比")
	flag.Parse()
	percentage, err := coverage(*profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("statement coverage: %.1f%% (minimum %.1f%%)\n", percentage, *minimum)
	if percentage+1e-9 < *minimum {
		fmt.Fprintf(os.Stderr, "coverage %.1f%% is below %.1f%%\n", percentage, *minimum)
		os.Exit(1)
	}
}

func coverage(path string) (float64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("打开 coverage profile: %w", err)
	}
	defer file.Close()

	var total, covered uint64
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if line == 1 {
			if !strings.HasPrefix(text, "mode:") {
				return 0, fmt.Errorf("coverage profile 缺少 mode header")
			}
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 3 {
			return 0, fmt.Errorf("coverage profile 第 %d 行格式无效", line)
		}
		statements, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("coverage profile 第 %d 行 statement 无效: %w", line, parseErr)
		}
		count, parseErr := strconv.ParseUint(fields[2], 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("coverage profile 第 %d 行 count 无效: %w", line, parseErr)
		}
		total += statements
		if count > 0 {
			covered += statements
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("读取 coverage profile: %w", err)
	}
	if total == 0 {
		return 0, fmt.Errorf("coverage profile 没有可统计语句")
	}
	return float64(covered) * 100 / float64(total), nil
}
