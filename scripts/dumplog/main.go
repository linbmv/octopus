// Command dumplog 是一个只读排查脚本，用于检查 octopus relay_logs 表的结构，
// 并按 model / channel 关键字查看最近的转发记录。
//
// 由 P0-1 从被临时覆盖的 main.go 保全而来：数据库路径改为参数化、处理打开错误、
// 使用参数化查询替代字符串拼接。仅用于本地排查，不属于正式服务入口。
//
// 用法：
//
//	go run ./scripts/dumplog -db /path/to/data.db -model claude-opus -channel nyrouter -limit 5
package main

import (
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	dbPath := flag.String("db", "", "SQLite 数据库文件路径（必填），例如 ./data/data.db")
	modelKeyword := flag.String("model", "", "按 actual_model_name 模糊匹配的关键字（可选）")
	channelKeyword := flag.String("channel", "", "按 channel_name 模糊匹配的关键字（可选）")
	limit := flag.Int("limit", 5, "每个查询返回的最大记录数")
	flag.Parse()

	if *dbPath == "" {
		log.Fatal("必须通过 -db 指定数据库路径")
	}

	db, err := gorm.Open(sqlite.Open(*dbPath+"?_pragma=busy_timeout(5000)"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}

	// 表结构
	type column struct {
		Name string
		Type string
	}
	var cols []column
	if err := db.Raw("PRAGMA table_info(relay_logs)").Scan(&cols).Error; err != nil {
		log.Fatalf("读取 relay_logs 表结构失败: %v", err)
	}
	fmt.Println("=== relay_logs 列 ===")
	for _, c := range cols {
		fmt.Printf("  %s : %s\n", c.Name, c.Type)
	}

	// 从表结构里挑出与查询相关的列，动态拼 SELECT 列名（列名来自 schema，非用户输入，安全）。
	var names []string
	for _, c := range cols {
		if strings.Contains(c.Name, "status") || strings.Contains(c.Name, "code") ||
			strings.Contains(c.Name, "model") || strings.Contains(c.Name, "channel") ||
			strings.Contains(c.Name, "request") {
			names = append(names, c.Name)
		}
	}
	if len(names) == 0 {
		fmt.Println("\n未找到可展示的相关列，退出。")
		return
	}
	fmt.Println("\n查询列:", names)
	sel := strings.Join(names, ", ")

	dump := func(title, whereCol, keyword string) {
		if keyword == "" {
			return
		}
		fmt.Printf("\n=== %s 近期 %d 条记录 ===\n", title, *limit)
		var rows []map[string]any
		// 列名来自 schema 白名单；关键字与 limit 用占位符参数化，避免注入。
		query := fmt.Sprintf("SELECT %s FROM relay_logs WHERE %s LIKE ? ORDER BY id DESC LIMIT ?", sel, whereCol)
		if err := db.Raw(query, "%"+keyword+"%", *limit).Scan(&rows).Error; err != nil {
			fmt.Printf("  查询失败: %v\n", err)
			return
		}
		for _, r := range rows {
			fmt.Printf("  %+v\n", r)
		}
	}

	dump(fmt.Sprintf("model 含 %q", *modelKeyword), "actual_model_name", *modelKeyword)
	dump(fmt.Sprintf("channel 含 %q", *channelKeyword), "channel_name", *channelKeyword)
}
