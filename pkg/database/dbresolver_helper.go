// Copyright 2025 Arcentra Team
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

package database

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/plugin/dbresolver"
)

// Read forces the query to use replicas (read-only)
// Usage: db.Clauses(Read()).Find(&users)
func Read() dbresolver.Operation {
	return dbresolver.Read
}

// Write forces the query to use sources (write)
// Usage: db.Clauses(Write()).First(&user)
func Write() dbresolver.Operation {
	return dbresolver.Write
}

// Use specifies which resolver to use (for named resolvers)
// Usage: db.Clauses(Use("secondary")).Find(&orders)
func Use(name string) clause.Expression {
	return dbresolver.Use(name)
}

// ReadDB returns a DB instance configured for read operations (replicas)
// This is a convenience method for read-only queries
func ReadDB(db *gorm.DB) *gorm.DB {
	return db.Clauses(dbresolver.Read)
}

// WriteDB returns a DB instance configured for write operations (sources)
// This is a convenience method for write operations
func WriteDB(db *gorm.DB) *gorm.DB {
	return db.Clauses(dbresolver.Write)
}

// UseResolver returns a DB instance configured to use a specific named resolver
// This is useful when you have multiple resolvers registered
func UseResolver(db *gorm.DB, resolverName string) *gorm.DB {
	return db.Clauses(dbresolver.Use(resolverName))
}

// UseResolverWrite returns a DB instance configured to use a specific named resolver in write mode
func UseResolverWrite(db *gorm.DB, resolverName string) *gorm.DB {
	return db.Clauses(dbresolver.Use(resolverName), dbresolver.Write)
}

// UseResolverRead returns a DB instance configured to use a specific named resolver in read mode
func UseResolverRead(db *gorm.DB, resolverName string) *gorm.DB {
	return db.Clauses(dbresolver.Use(resolverName), dbresolver.Read)
}
