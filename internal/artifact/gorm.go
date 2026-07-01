package artifact

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GormStore struct {
	db *gorm.DB
}

type artifactRecord struct {
	Module     string     `gorm:"column:module;primaryKey"`
	Version    string     `gorm:"column:version;primaryKey"`
	MatrixStr  string     `gorm:"column:matrix_str;primaryKey"`
	SourceType string     `gorm:"column:source_type;not null"`
	SourceURL  string     `gorm:"column:source_url;not null"`
	Type       string     `gorm:"column:type;not null"`
	Metadata   string     `gorm:"column:metadata;not null"`
	Checksum   string     `gorm:"column:checksum;not null"`
	CreatedAt  time.Time  `gorm:"column:created_at;not null"`
	ExpiresAt  *time.Time `gorm:"column:expires_at"`
}

func (artifactRecord) TableName() string {
	return "artifacts"
}

func NewGormStore(db *gorm.DB) (*GormStore, error) {
	if err := db.AutoMigrate(&artifactRecord{}); err != nil {
		return nil, fmt.Errorf("migrate artifacts table: %w", err)
	}
	return &GormStore{db: db}, nil
}

func (s *GormStore) Get(ctx context.Context, key Key) (Artifact, bool, error) {
	return s.get(ctx, key)
}

func (s *GormStore) Put(ctx context.Context, key Key, artifact Artifact) (Artifact, error) {
	record := artifactRecord{
		Module:     key.Module,
		Version:    key.Version,
		MatrixStr:  key.MatrixStr,
		SourceType: artifact.Source.Type,
		SourceURL:  artifact.Source.URL,
		Type:       artifact.Type,
		Metadata:   artifact.Metadata,
		Checksum:   artifact.Checksum,
		CreatedAt:  time.Now().UTC(),
	}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "module"},
			{Name: "version"},
			{Name: "matrix_str"},
		},
		DoUpdates: clause.Set{
			{
				Column: clause.Column{Name: "source_type"},
				Value:  clause.Column{Table: clause.CurrentTable, Name: "source_type"},
			},
		},
	}, clause.Returning{}).Create(&record).Error
	if err != nil {
		return Artifact{}, fmt.Errorf("insert artifact: %w", err)
	}
	return record.artifact(), nil
}

func (s *GormStore) GetOrUpdate(ctx context.Context, key Key, update func() (Artifact, error)) (Artifact, error) {
	var record artifactRecord
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("module = ? AND version = ? AND matrix_str = ?", key.Module, key.Version, key.MatrixStr).
			First(&record).Error
		if err != nil {
			return fmt.Errorf("lock artifact: %w", err)
		}
		if record.SourceURL != "" {
			return nil
		}

		artifact, err := update()
		if err != nil {
			return err
		}
		record.SourceType = artifact.Source.Type
		record.SourceURL = artifact.Source.URL
		record.Type = artifact.Type
		record.Metadata = artifact.Metadata
		record.Checksum = artifact.Checksum
		record.CreatedAt = time.Now().UTC()

		err = tx.Model(&artifactRecord{}).
			Where("module = ? AND version = ? AND matrix_str = ?", key.Module, key.Version, key.MatrixStr).
			Updates(map[string]any{
				"source_type": record.SourceType,
				"source_url":  record.SourceURL,
				"type":        record.Type,
				"metadata":    record.Metadata,
				"checksum":    record.Checksum,
				"created_at":  record.CreatedAt,
			}).Error
		if err != nil {
			return fmt.Errorf("update artifact: %w", err)
		}
		return nil
	})
	if err != nil {
		return Artifact{}, err
	}
	return record.artifact(), nil
}

func (s *GormStore) Delete(ctx context.Context, key Key) error {
	err := s.db.WithContext(ctx).
		Where("module = ? AND version = ? AND matrix_str = ?", key.Module, key.Version, key.MatrixStr).
		Delete(&artifactRecord{}).Error
	if err != nil {
		return fmt.Errorf("delete artifact: %w", err)
	}
	return nil
}

func (s *GormStore) get(ctx context.Context, key Key) (Artifact, bool, error) {
	var record artifactRecord
	err := s.db.WithContext(ctx).
		Where("module = ? AND version = ? AND matrix_str = ?", key.Module, key.Version, key.MatrixStr).
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Artifact{}, false, nil
	}
	if err != nil {
		return Artifact{}, false, fmt.Errorf("get artifact: %w", err)
	}
	if record.SourceURL == "" {
		return Artifact{}, false, nil
	}
	return record.artifact(), true, nil
}

func (r artifactRecord) artifact() Artifact {
	return Artifact{
		Source: Source{
			Type: r.SourceType,
			URL:  r.SourceURL,
		},
		Type:     r.Type,
		Metadata: r.Metadata,
		Checksum: r.Checksum,
	}
}
