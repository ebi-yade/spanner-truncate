//
// Copyright 2020 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package truncate

import (
	"context"

	"cloud.google.com/go/spanner"
)

// deleteActionType is action type on parent delete.
type deleteActionType int

const (
	deleteActionUndefined     deleteActionType = iota // Undefined action type on parent delete.
	deleteActionCascadeDelete                         // Cascade delete type on parent delete.
	deleteActionNoAction                              // No action type on parent delete.
)

// tableSchema represents table metadata and relationships.
type tableSchema struct {
	tableName string

	// Parent / Child relationship.
	parentTableName      string
	parentOnDeleteAction deleteActionType

	// Foreign Key Reference.
	referencedBy []string
}

// indexSchema represents secondary index metadata.
type indexSchema struct {
	indexName string

	// Table name on which the index is defined.
	baseTableName string

	// Table name the index interleaved in. If blank, the index is a global index.
	parentTableName string
}

func fetchTableSchemas(ctx context.Context, client *spanner.Client, targetTables, excludeTables []string) ([]*tableSchema, error) {
	// This query fetches the table metadata and relationships.
	iter := client.Single().Query(ctx, spanner.NewStatement(`
		WITH FKReferences AS (
			SELECT CCU.TABLE_NAME AS Referenced, ARRAY_AGG(TC.TABLE_NAME) AS Referencing
			FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS as TC
			INNER JOIN INFORMATION_SCHEMA.CONSTRAINT_COLUMN_USAGE AS CCU ON TC.CONSTRAINT_NAME = CCU.CONSTRAINT_NAME
			WHERE TC.TABLE_CATALOG = '' AND TC.TABLE_SCHEMA = '' AND TC.CONSTRAINT_TYPE = 'FOREIGN KEY' AND CCU.TABLE_CATALOG = '' AND CCU.TABLE_SCHEMA = ''
			GROUP BY CCU.TABLE_NAME
		)
		SELECT T.TABLE_NAME, T.PARENT_TABLE_NAME, T.ON_DELETE_ACTION, IF(F.Referencing IS NULL, ARRAY<STRING>[], F.Referencing) AS referencedBy
		FROM INFORMATION_SCHEMA.TABLES AS T
		LEFT OUTER JOIN FKReferences AS F ON T.TABLE_NAME = F.Referenced
		WHERE T.TABLE_CATALOG = "" AND T.TABLE_SCHEMA = "" AND T.TABLE_TYPE = "BASE TABLE"
		ORDER BY T.TABLE_NAME ASC
	`))

	truncateAll := true
	targets := make(map[string]bool, len(targetTables))
	excludes := make(map[string]bool, len(excludeTables))
	if len(targetTables) > 0 || len(excludeTables) > 0 {
		truncateAll = false
		for _, t := range targetTables {
			targets[t] = true
		}
		for _, t := range excludeTables {
			excludes[t] = true
		}
	}

	var tables []*tableSchema
	if err := iter.Do(func(r *spanner.Row) error {
		var (
			tableName    string
			parent       spanner.NullString
			deleteAction spanner.NullString
			referencedBy []string
		)
		if err := r.Columns(&tableName, &parent, &deleteAction, &referencedBy); err != nil {
			return err
		}

		if !truncateAll {
			if len(excludes) != 0 {
				if _, ok := excludes[tableName]; ok {
					return nil
				}
			} else {
				if _, ok := targets[tableName]; !ok {
					return nil
				}
			}
		}

		var parentTableName string
		if parent.Valid {
			parentTableName = parent.StringVal
		}

		var typ deleteActionType
		if deleteAction.Valid {
			switch deleteAction.StringVal {
			case "CASCADE":
				typ = deleteActionCascadeDelete
			case "NO ACTION":
				typ = deleteActionNoAction
			}
		}

		tables = append(tables, &tableSchema{
			tableName:            tableName,
			parentTableName:      parentTableName,
			parentOnDeleteAction: typ,
			referencedBy:         referencedBy,
		})
		return nil
	}); err != nil {
		return nil, err
	}

	return tables, nil
}

func fetchIndexSchemas(ctx context.Context, client *spanner.Client) ([]*indexSchema, error) {
	// This query fetches defined indexes.
	iter := client.Single().Query(ctx, spanner.NewStatement(`
		SELECT INDEX_NAME, TABLE_NAME, PARENT_TABLE_NAME FROM INFORMATION_SCHEMA.INDEXES
		WHERE INDEX_TYPE = 'INDEX' AND TABLE_CATALOG = '' AND TABLE_SCHEMA = '';
	`))

	var indexes []*indexSchema
	if err := iter.Do(func(r *spanner.Row) error {
		var (
			indexName     string
			baseTableName string
			parent        spanner.NullString
		)
		if err := r.Columns(&indexName, &baseTableName, &parent); err != nil {
			return err
		}

		var parentTableName string
		if parent.Valid {
			parentTableName = parent.StringVal
		}

		indexes = append(indexes, &indexSchema{
			indexName:       indexName,
			baseTableName:   baseTableName,
			parentTableName: parentTableName,
		})
		return nil
	}); err != nil {
		return nil, err
	}

	return indexes, nil
}
