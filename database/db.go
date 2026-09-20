package database

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"io/fs"
	"os"
	"path"
	"strings"
	"x-ui/config"
	"x-ui/database/model"
	uilogger "x-ui/logger"
)

var db *gorm.DB

func initUser() error {
	err := db.AutoMigrate(&model.User{})
	if err != nil {
		return err
	}
	var count int64
	err = db.Model(&model.User{}).Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		user := &model.User{
			Username: "admin",
			Password: "admin",
		}
		return db.Create(user).Error
	}
	return nil
}

func initInbound() error {
	return db.AutoMigrate(&model.Inbound{})
}

func initSetting() error {
	return db.AutoMigrate(&model.Setting{})
}

func initTrafficSnapshot() error {
	err := db.AutoMigrate(&model.TrafficSnapshot{})
	if err != nil {
		// 兼容运行过旧版 fork 的库：SQLite 索引名全库唯一，残留的同名索引会让
		// "建索引"阶段报 already exists。仅当表确实存在时才容忍（此时只是缺索引）；
		// 表没建出来必须上抛，不能让大盘在"启动成功"的假象下永久没有数据。
		if !strings.Contains(err.Error(), "already exists") || !db.Migrator().HasTable(&model.TrafficSnapshot{}) {
			return err
		}
		uilogger.Warning("traffic snapshot migrate skipped:", err)
	}
	// 兜底：确保概览与清理查询依赖的 created_at 索引存在
	// （上面的容忍分支会中断 AutoMigrate 的建索引步骤）
	if !db.Migrator().HasIndex(&model.TrafficSnapshot{}, "idx_traffic_snapshot_created_at") {
		if e := db.Migrator().CreateIndex(&model.TrafficSnapshot{}, "idx_traffic_snapshot_created_at"); e != nil {
			uilogger.Warning("create traffic snapshot created_at index failed:", e)
		}
	}
	return nil
}

func InitDB(dbPath string) error {
	dir := path.Dir(dbPath)
	err := os.MkdirAll(dir, fs.ModeDir)
	if err != nil {
		return err
	}

	var gormLogger logger.Interface

	if config.IsDebug() {
		gormLogger = logger.Default
	} else {
		gormLogger = logger.Discard
	}

	c := &gorm.Config{
		Logger: gormLogger,
	}
	db, err = gorm.Open(sqlite.Open(dbPath), c)
	if err != nil {
		return err
	}

	err = initUser()
	if err != nil {
		return err
	}
	err = initInbound()
	if err != nil {
		return err
	}
	err = initSetting()
	if err != nil {
		return err
	}
	err = initTrafficSnapshot()
	if err != nil {
		return err
	}

	return nil
}

func GetDB() *gorm.DB {
	return db
}

func IsNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}
