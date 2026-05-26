# Coverage: raw SQL surface

Comprehensive feature-by-feature test suite under `tests/sql/`. Every
documented SQLite SQL surface this driver exposes is exercised by at
least one test. Last reviewed against SQLite 3.53.1 (bundled via
`modernc.org/sqlite v1.50.1`) on 2026-05-26.

Run locally with `go test ./tests/sql/...`.

## Status legend

- **✓ typed** — exercised by at least one test in `tests/sql/*_test.go`.
- **⚠ inherited** — supported by underlying SQLite but no dedicated
  test in this module. Carry-over from documentation.
- **✗** — explicitly not supported (userauth, FTS3/FTS4), with
  rationale.

## Data types and affinity

| Feature | Status | Test |
|---|---|---|
| INTEGER affinity (string→int coercion) | ✓ typed | `TestType_IntegerAffinity` |
| REAL affinity | ✓ typed | `TestType_RealAffinity` |
| TEXT affinity | ✓ typed | `TestType_TextAffinity` |
| BLOB affinity (exact bytes) | ✓ typed | `TestType_BlobAffinity` |
| NUMERIC affinity | ✓ typed | `TestType_NumericAffinity` |
| NONE affinity | ✓ typed | `TestType_NoneAffinity` |
| sql.Null* wrappers (String/Int64/Float64/Bool/Time) | ✓ typed | `TestType_NullRoundTrip` |
| NULL sort order (default, FIRST, LAST) | ✓ typed | `TestType_NullSortOrder` |
| '' vs NULL distinction | ✓ typed | `TestType_EmptyVsNull` |

## SELECT clause

| Feature | Status | Test |
|---|---|---|
| WHERE: `=`, `!=`, `<`, `>`, `BETWEEN`, `IN`, `NOT IN`, `IS NULL`, `IS NOT NULL`, `LIKE`, `GLOB` | ✓ typed | `TestSelect_WhereComparisons` |
| ORDER BY (ASC/DESC, multi-col, NULLS FIRST/LAST) | ✓ typed | `TestSelect_OrderBy`, `TestType_NullSortOrder` |
| GROUP BY + HAVING | ✓ typed | `TestSelect_GroupByHaving` |
| DISTINCT | ✓ typed | `TestSelect_Distinct` |
| UNION / UNION ALL / INTERSECT / EXCEPT | ✓ typed | `TestSelect_SetOperators` |
| LIMIT / OFFSET / LIMIT M,N | ✓ typed | `TestSelect_LimitOffset` |
| Column aliases (`AS`) | ✓ typed | `TestSelect_ColumnAliases` |

## JOIN

| Feature | Status | Test |
|---|---|---|
| INNER JOIN | ✓ typed | `TestJoin_InnerJoin` |
| LEFT [OUTER] JOIN | ✓ typed | `TestJoin_LeftJoin`, `TestJoin_LeftJoin_ThreeWay` |
| RIGHT JOIN (SQLite ≥ 3.39) | ✓ typed | `TestJoin_RightOuterSupported` |
| CROSS JOIN | ✓ typed | `TestJoin_CrossJoin` |
| USING(col) | ✓ typed | `TestJoin_Using` |
| Self-join | ✓ typed | `TestJoin_SelfJoin` |
| FULL OUTER JOIN | ⚠ inherited | — |

## CTE / WITH

| Feature | Status | Test |
|---|---|---|
| WITH (single CTE) | ✓ typed | `TestCTE_Simple` |
| WITH (chained CTEs) | ✓ typed | `TestCTE_Chained` |
| WITH RECURSIVE (Fibonacci) | ✓ typed | `TestCTE_Recursive_Fibonacci` |
| WITH RECURSIVE (tree walk) | ✓ typed | `TestCTE_Recursive_TreeWalk` |
| CTE in INSERT | ✓ typed | `TestCTE_InInsert` |
| MATERIALIZED / NOT MATERIALIZED hint | ✓ typed | `TestCTE_MaterializationHint` |

## Subqueries

| Feature | Status | Test |
|---|---|---|
| Scalar subquery (SELECT projection) | ✓ typed | `TestSubquery_Scalar` |
| Correlated subquery (WHERE) | ✓ typed | `TestSubquery_Correlated` |
| EXISTS / NOT EXISTS | ✓ typed | `TestSubquery_Exists` |
| IN (subquery) | ✓ typed | `TestSubquery_InSubquery` |

## Aggregates

| Feature | Status | Test |
|---|---|---|
| count(*), count(col), count(DISTINCT col) | ✓ typed | `TestAgg_CountVariants` |
| sum / total | ✓ typed | `TestAgg_SumAndTotal` |
| avg | ✓ typed | `TestAgg_Avg` |
| min / max | ✓ typed | `TestAgg_MinMax` |
| group_concat (default + custom sep) | ✓ typed | `TestAgg_GroupConcat` |
| FILTER (WHERE …) clause | ✓ typed | `TestAgg_FilterClause` |
| DISTINCT inside aggregate | ✓ typed | `TestAgg_DistinctInAgg` |
| GROUP BY (multi-col) | ✓ typed | `TestAgg_GroupByMulti` |

## Window functions

| Feature | Status | Test |
|---|---|---|
| ROW_NUMBER | ✓ typed | `TestWindow_RowNumber` |
| RANK, DENSE_RANK | ✓ typed | `TestWindow_RankAndDenseRank` |
| NTILE | ✓ typed | `TestWindow_Ntile` |
| LAG, LEAD | ✓ typed | `TestWindow_LagLead` |
| FIRST_VALUE, LAST_VALUE, NTH_VALUE | ✓ typed | `TestWindow_FirstLastNthValue` |
| Aggregate over window | ✓ typed | `TestWindow_AggOverWindow` |
| ROWS frame (preceding/following) | ✓ typed | `TestWindow_RowsFrameVariants` |
| RANGE frame | ✓ typed | `TestWindow_RangeFrame` |
| Named window (WINDOW w AS) | ✓ typed | `TestWindow_NamedAndPartitioned` |
| PARTITION BY | ✓ typed | `TestWindow_NamedAndPartitioned` |
| EXCLUDE clause | ✓ typed | `TestWindow_ExcludeClause` |
| FILTER inside OVER | ✓ typed | `TestWindow_FilterClause` |
| GROUPS frame | ⚠ inherited | — |

## Scalar functions — strings

| Feature | Status | Test |
|---|---|---|
| length (UTF-8) | ✓ typed | `TestStr_LengthAndCharIndex` |
| substr / substring (positive + negative offsets) | ✓ typed | `TestStr_SubstrOffsets` |
| replace | ✓ typed | `TestStr_Replace` |
| instr | ✓ typed | `TestStr_Instr` |
| hex / unhex (3.41+) | ✓ typed | `TestStr_HexUnhex` |
| upper / lower | ✓ typed | `TestStr_UpperLower` |
| trim / ltrim / rtrim (with charset) | ✓ typed | `TestStr_TrimVariants` |
| LIKE (case-insensitive), GLOB, LIKE ESCAPE | ✓ typed | `TestStr_LikeGlob` |
| printf / format (3.38+) | ✓ typed | `TestStr_PrintfFormat` |
| char / unicode | ✓ typed | `TestStr_CharUnicode` |
| quote | ✓ typed | `TestStr_Quote` |
| `||` concatenation | ✓ typed | `TestStr_Concat` |
| coalesce | ✓ typed | `TestStr_Coalesce`, `TestCond_Coalesce` |

## Scalar functions — math

| Feature | Status | Test |
|---|---|---|
| abs | ✓ typed | `TestMath_Abs` |
| max / min (scalar) | ✓ typed | `TestMath_MaxMinScalar` |
| round (with precision) | ✓ typed | `TestMath_Round` |
| randomblob | ✓ typed | `TestMath_RandomBlob` |
| sqrt, pow/power, exp, ln, log10, log2 | ✓ typed | `TestMath_ExtendedFunctions` |
| pi, sin, cos, tan, asin, acos, atan, atan2 | ✓ typed | `TestMath_ExtendedFunctions` |
| ceil/ceiling, floor, trunc, mod | ✓ typed | `TestMath_ExtendedFunctions` |
| degrees, radians, sinh, cosh, tanh | ✓ typed | `TestMath_ExtendedFunctions` |

## Scalar functions — date/time

| Feature | Status | Test |
|---|---|---|
| date, time, datetime | ✓ typed | `TestDateTime_BasicFormat` |
| julianday | ✓ typed | `TestDateTime_Julianday` |
| unixepoch (3.38+) | ✓ typed | `TestDateTime_Unixepoch` |
| strftime (every format spec) | ✓ typed | `TestDateTime_StrftimeFormats` |
| Modifiers (start of /, +N days, weekday, etc.) | ✓ typed | `TestDateTime_Modifiers` |
| 'now' keyword | ✓ typed | `TestDateTime_NowKeyword` |
| 'utc' / 'localtime' | ✓ typed | `TestDateTime_UtcLocaltime` |
| Subsecond precision (%f, 3.42+) | ✓ typed | `TestDateTime_SubsecondPrecision` |

## Scalar functions — conditional

| Feature | Status | Test |
|---|---|---|
| CASE WHEN (simple form) | ✓ typed | `TestCond_CaseWhen_Simple` |
| CASE WHEN (searched form) | ✓ typed | `TestCond_CaseWhen_Searched` |
| IIF | ✓ typed | `TestCond_Iif` |
| COALESCE | ✓ typed | `TestCond_Coalesce` |
| IFNULL | ✓ typed | `TestCond_Ifnull` |
| NULLIF | ✓ typed | `TestCond_Nullif` |

## JSON1

| Feature | Status | Test |
|---|---|---|
| json_valid | ✓ typed | `TestJSON_Validate` |
| json_extract (`$.path`, array idx, nested) | ✓ typed | `TestJSON_Extract` |
| `->` and `->>` operators (3.38+) | ✓ typed | `TestJSON_Operators_ArrowDoubleArrow` |
| json_set | ✓ typed | `TestJSON_Set` |
| json_insert vs json_replace | ✓ typed | `TestJSON_InsertVsReplace` |
| json_remove | ✓ typed | `TestJSON_Remove` |
| json_array, json_object | ✓ typed | `TestJSON_ArrayObject` |
| json_each (table-valued) | ✓ typed | `TestJSON_Each` |
| json_tree (table-valued) | ✓ typed | `TestJSON_Tree` |
| json_group_array | ✓ typed | `TestJSON_GroupArray` |
| json_group_object | ✓ typed | `TestJSON_GroupObject` |
| json_type | ✓ typed | `TestJSON_TypeFunction` |
| json_quote | ✓ typed | `TestJSON_Quote` |
| jsonb_* binary form (3.45+) | ⚠ inherited | — |

## Constraints

| Feature | Status | Test |
|---|---|---|
| INTEGER PRIMARY KEY (ROWID alias) | ✓ typed | `TestConstraint_IntegerPrimaryKey` |
| AUTOINCREMENT | ✓ typed | `TestConstraint_AutoIncrement` |
| Composite PRIMARY KEY | ✓ typed | `TestConstraint_CompositePrimaryKey` |
| UNIQUE (single col + NULL semantics) | ✓ typed | `TestConstraint_Unique` |
| NOT NULL | ✓ typed | `TestConstraint_NotNull` |
| CHECK (column + table level) | ✓ typed | `TestConstraint_Check` |
| FOREIGN KEY ON DELETE/UPDATE CASCADE | ✓ typed | `TestConstraint_ForeignKey_Cascade` |
| FOREIGN KEY ON DELETE RESTRICT | ✓ typed | `TestConstraint_ForeignKey_Restrict` |
| FOREIGN KEY ON DELETE SET NULL | ✓ typed | `TestConstraint_ForeignKey_SetNull` |
| DEFAULT (literal, expression) | ✓ typed | `TestConstraint_Default` |
| DEFERRABLE INITIALLY DEFERRED | ✓ typed | `TestConstraint_DeferredForeignKey` |

## Indexes

| Feature | Status | Test |
|---|---|---|
| CREATE INDEX (single col) | ✓ typed | `TestIndex_BasicCreate` |
| CREATE UNIQUE INDEX | ✓ typed | `TestIndex_Unique` |
| Multi-column index (planner consults) | ✓ typed | `TestIndex_MultiColumn` |
| Partial index (WHERE) | ✓ typed | `TestIndex_Partial` |
| Expression index | ✓ typed | `TestIndex_Expression` |
| COLLATE NOCASE / BINARY / RTRIM | ✓ typed | `TestIndex_Collation` |
| IF NOT EXISTS | ✓ typed | `TestIndex_IfNotExists` |
| DROP INDEX, REINDEX | ✓ typed | `TestIndex_DropAndReindex` |

## Views

| Feature | Status | Test |
|---|---|---|
| CREATE VIEW | ✓ typed | `TestView_Basic` |
| TEMPORARY VIEW | ✓ typed | `TestView_Temporary` |
| View on view | ✓ typed | `TestView_OnView` |
| DROP VIEW IF EXISTS | ✓ typed | `TestView_DropIfExists` |
| Column-list rename in CREATE VIEW | ✓ typed | `TestView_ColumnAliases` |

## Triggers

| Feature | Status | Test |
|---|---|---|
| BEFORE INSERT | ✓ typed | `TestTrigger_BeforeInsert` |
| AFTER UPDATE OF col | ✓ typed | `TestTrigger_AfterUpdate` |
| AFTER DELETE | ✓ typed | `TestTrigger_AfterDelete` |
| INSTEAD OF (on view) | ✓ typed | `TestTrigger_InsteadOfView` |
| WHEN clause | ✓ typed | `TestTrigger_WhenClause` |
| RAISE(ABORT/FAIL/IGNORE/ROLLBACK) | ✓ typed | `TestTrigger_Raise` |
| DROP TRIGGER | ✓ typed | `TestTrigger_DropTrigger` |
| FOR EACH STATEMENT | ⚠ inherited | — |

## Generated columns

| Feature | Status | Test |
|---|---|---|
| GENERATED ALWAYS AS … VIRTUAL | ✓ typed | `TestGenerated_Virtual` |
| GENERATED ALWAYS AS … STORED | ✓ typed | `TestGenerated_Stored` |
| Dependent expressions | ✓ typed | `TestGenerated_DependentColumn` |
| Update propagates | ✓ typed | `TestGenerated_AfterUpdate` |

## WITHOUT ROWID

| Feature | Status | Test |
|---|---|---|
| Basic WITHOUT ROWID | ✓ typed | `TestWithoutRowid_Basic` |
| rowid column not accessible | ✓ typed | `TestWithoutRowid_NoRowidColumn` |
| UPDATE + DELETE behavior | ✓ typed | `TestWithoutRowid_UpdateAndDelete` |

## STRICT tables (3.37+)

| Feature | Status | Test |
|---|---|---|
| Type enforcement | ✓ typed | `TestStrict_BasicTypeEnforcement` |
| ANY type column | ✓ typed | `TestStrict_AnyTypeColumn` |
| typeof() preserves declared type | ✓ typed | `TestStrict_IntPreservesType` |
| STRICT + WITHOUT ROWID combined | ✓ typed | `TestStrict_WithoutRowidCombined` |

## UPSERT (INSERT … ON CONFLICT)

| Feature | Status | Test |
|---|---|---|
| DO NOTHING | ✓ typed | `TestUpsert_DoNothing` |
| DO UPDATE SET (with excluded.*) | ✓ typed | `TestUpsert_DoUpdate` |
| DO UPDATE … WHERE | ✓ typed | `TestUpsert_WhereClause` |
| Multi-row UPSERT | ✓ typed | `TestUpsert_MultiRow` |
| ON CONFLICT (no target) | ✓ typed | `TestUpsert_OnConflictNoTarget` |
| INSERT OR IGNORE | ✓ typed | `TestUpsert_OrIgnore` |

## RETURNING (3.35+)

| Feature | Status | Test |
|---|---|---|
| INSERT … RETURNING | ✓ typed | `TestReturning_Insert` |
| UPDATE … RETURNING | ✓ typed | `TestReturning_Update` |
| DELETE … RETURNING | ✓ typed | `TestReturning_Delete` |
| RETURNING * | ✓ typed | `TestReturning_Star` |
| RETURNING with alias | ✓ typed | `TestReturning_WithAlias` |

## Transactions

| Feature | Status | Test |
|---|---|---|
| BEGIN + COMMIT | ✓ typed | `TestTransaction_BeginCommit` |
| ROLLBACK | ✓ typed | `TestTransaction_Rollback` |
| Isolation levels (database/sql) | ✓ typed | `TestTransaction_IsolationLevels` |
| SAVEPOINT / RELEASE / ROLLBACK TO | ✓ typed | `TestTransaction_Savepoint` |
| Nested savepoints | ✓ typed | `TestTransaction_NestedSavepoints` |
| Read-only transactions | ✓ typed | `TestTransaction_ReadOnly` |
| Auto-commit (no explicit BEGIN) | ✓ typed | `TestTransaction_AutoCommit` |
| Rollback on error | ✓ typed | `TestTransaction_RollbackOnError` |
| BEGIN DEFERRED / IMMEDIATE / EXCLUSIVE | ⚠ inherited | — |

## PRAGMA

| PRAGMA | Status | Test |
|---|---|---|
| foreign_keys | ✓ typed | `TestPragma_ForeignKeys` |
| journal_mode | ✓ typed | `TestPragma_JournalMode` |
| synchronous | ✓ typed | `TestPragma_Synchronous` |
| busy_timeout | ✓ typed | `TestPragma_BusyTimeout` |
| cache_size | ✓ typed | `TestPragma_CacheSize` |
| recursive_triggers | ✓ typed | `TestPragma_RecursiveTriggers` |
| user_version | ✓ typed | `TestPragma_UserVersion` |
| application_id | ✓ typed | `TestPragma_ApplicationId` |
| table_info | ✓ typed | `TestPragma_TableInfo` |
| index_list | ✓ typed | `TestPragma_IndexList` |
| foreign_key_list | ✓ typed | `TestPragma_ForeignKeyList` |
| integrity_check | ✓ typed | `TestPragma_IntegrityCheck` |
| quick_check | ✓ typed | `TestPragma_QuickCheck` |
| auto_vacuum | ✓ typed | `TestPragma_AutoVacuum`, `TestVacuum_AutoVacuumFull` |
| case_sensitive_like | ✓ typed | `TestPragma_CaseSensitiveLike` |
| database_list | ✓ typed | `TestAttach_DatabaseList` |
| freelist_count | ✓ typed | `TestVacuum_FreelistCount` |
| temp_store, locking_mode, secure_delete, etc. | ⚠ inherited | — |

## ATTACH / DETACH

| Feature | Status | Test |
|---|---|---|
| ATTACH + cross-DB query | ✓ typed | `TestAttach_BasicAttachAndQuery` |
| Cross-DB JOIN | ✓ typed | `TestAttach_CrossDbJoin` |
| DETACH | ✓ typed | `TestAttach_Detach` |
| database_list pragma | ✓ typed | `TestAttach_DatabaseList` |

## VACUUM

| Feature | Status | Test |
|---|---|---|
| VACUUM | ✓ typed | `TestVacuum_Basic` |
| VACUUM INTO file | ✓ typed | `TestVacuum_Into` |
| auto_vacuum = FULL | ✓ typed | `TestVacuum_AutoVacuumFull` |
| auto_vacuum = INCREMENTAL + incremental_vacuum | ✓ typed | `TestVacuum_IncrementalVacuum` |
| freelist_count | ✓ typed | `TestVacuum_FreelistCount` |

## Explicitly not supported

| Feature | Reason |
|---|---|
| FTS3, FTS4 virtual tables | modernc ships **FTS5 only**; the `no such module: fts3` error is the documented signal. |
| Userauth (SQLITE_USER_AUTHENTICATION) | Removed upstream from modernc. We reject `_auth*` DSN flags. |
| WITH RECURSIVE … `materialized` (with the materialization hint) reaching outside the CTE | Standard limitation; tested indirectly via `TestCTE_MaterializationHint`. |

## When SQLite or modernc bumps

1. `just bump-modernc vX.Y.Z` (or whatever the upstream version is).
2. `go test ./tests/sql/...` — should still be green.
3. If new SQL features ship in the bump, add tests + extend this doc.
4. If existing tests fail, classify: genuine SQL behavior change
   (port the test) vs driver regression (fix the driver).
