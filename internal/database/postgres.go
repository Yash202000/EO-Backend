package database

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/database/migrations"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/utils"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func Connect(cfg *config.DatabaseConfig) (*gorm.DB, error) {
	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=%s",
		cfg.Host, cfg.User, cfg.Password, cfg.DBName, cfg.Port, cfg.SSLMode,
	)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	DB = db
	log.Println("Database connected successfully")
	return db, nil
}

func Migrate(db *gorm.DB, cfg *config.Config) error {
	log.Println("Running database migrations...")
	// License table schema migration: the old single-PEM public_key column has
	// been replaced by a JWKS JSON blob. Dropping here is idempotent — AutoMigrate
	// will add the new `jwks` column on the next step. Any pre-existing license row
	// becomes non-verifiable; LoadFromDB gracefully falls back to cached state.
	if err := db.Exec(`ALTER TABLE IF EXISTS licenses DROP COLUMN IF EXISTS public_key`).Error; err != nil {
		log.Printf("Warning: failed to drop legacy licenses.public_key column: %v", err)
	}

	// Use session that disables FK constraints during migration to prevent auto-FK issues
	migrationDB := db.Session(&gorm.Session{})
	migrationDB.Config.DisableForeignKeyConstraintWhenMigrating = true

	err := migrationDB.AutoMigrate(
		&models.Permission{},
		&models.Role{},
		&models.Classification{},
		&models.ClassificationType{},
		&models.ClassificationCriticality{},
		&models.Location{},
		&models.Department{},
		&models.User{},
		&models.ActionLog{},
		&models.SystemAttachment{},
		&models.NotificationTemplate{},
		&models.NotificationLog{},
		&models.EscalationSLA{},
		// IncidentMerge MUST come before Incident (foreign key dependency)
		&models.IncidentMerge{},
		// Lookup models
		&models.LookupCategory{},
		&models.LookupValue{},
		// Workflow models
		&models.Workflow{},
		&models.WorkflowState{},
		&models.WorkflowTransition{},
		&models.TransitionRequirement{},
		&models.TransitionAction{},
		&models.TransitionFieldChange{},
		&models.CommentTemplate{},
		&models.FeedbackTemplate{},

		&models.TicketType{},
		&models.TicketTypeLink{},
		&models.Ticket{},
		&models.TicketTransition{},
		&models.TicketComment{},
		&models.TicketAttachment{},
		&models.TicketActivity{},
		// Incident models
		&models.Incident{},
		&models.IncidentComment{},
		&models.IncidentAttachment{},
		&models.IncidentFeedback{},
		&models.IncidentTransitionHistory{},
		&models.IncidentRevision{},
		&models.IncidentRejectionLog{},
		// Report models
		&models.Report{},
		&models.ReportExecution{},
		&models.ReportTemplate{},
		// Application Links
		&models.ApplicationLink{},
		&models.Config{},
		// Settings
		&models.Settings{},
		// Escalation Policies
		&models.EscalationPolicy{},
		&models.EscalationPolicyStep{},
		&models.EscalationPolicyStepTarget{},
		// Custom Escalation Groups
		&models.EscalationGroup{},
		&models.EscalationGroupTarget{},
		// Error Log
		&models.ErrorLog{},
		&models.ErrorLogHistory{},
		// Ready-to-Close expiry tracking
		&models.IncidentReadyToCloseEntry{},
		&models.DeviceToken{},
		// License Management
		&models.License{},
		// External Integration
		&models.IntegrationVariable{},
		&models.IntegrationScript{},
		&models.WorkflowStateTrigger{},
		&models.WorkflowTransitionTrigger{},
		&models.IntegrationExecutionLog{},
		&models.IncidentBridge{},
		&models.WebhookCallbackConfig{},
	)
	if err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	// Drop problematic foreign key constraints on incidents table
	// Merge relationships are tracked via incident_merges table, not direct FK
	log.Println("Cleaning up auto-generated foreign key constraints...")
	db.Exec("ALTER TABLE incidents DROP CONSTRAINT IF EXISTS fk_incident_merges_master_incident")
	db.Exec("ALTER TABLE incidents DROP CONSTRAINT IF EXISTS incident_merges_master_incident_id_fkey")
	db.Exec("ALTER TABLE incidents DROP CONSTRAINT IF EXISTS fk_incidents_master_incident")

	// Drop FK constraints on performed_by_id so system-triggered actions (uuid.Nil) are allowed.
	// System actions (e.g. Ready-to-Close auto-revert) do not have a real user performer.
	// Tables created later in this run (or only in some deployments) don't exist yet on a fresh DB.
	for _, c := range [][2]string{
		{"incident_transition_histories", "fk_incident_transition_histories_performed_by"},
		{"incident_transition_histories", "incident_transition_histories_performed_by_id_fkey"},
		{"incident_revisions", "fk_incident_revisions_performed_by"},
		{"incident_revisions", "incident_revisions_performed_by_id_fkey"},
	} {
		if db.Migrator().HasTable(c[0]) {
			db.Exec("ALTER TABLE " + c[0] + " DROP CONSTRAINT IF EXISTS " + c[1])
		}
	}

	db.Exec("ALTER TABLE lookup_categories ADD COLUMN IF NOT EXISTS redirect_url VARCHAR(500)")

	// Auth settings: show_forgot_password toggle (default true). AutoMigrate adds
	// the column, but ensure the default/backfill explicitly for existing DBs.
	db.Exec("ALTER TABLE settings ADD COLUMN IF NOT EXISTS show_forgot_password BOOLEAN DEFAULT TRUE")
	db.Exec("ALTER TABLE settings ALTER COLUMN show_forgot_password SET DEFAULT TRUE")
	db.Exec("UPDATE settings SET show_forgot_password = TRUE WHERE show_forgot_password IS NULL")

	// Notification template enhancements: add categorisation and bilingual-linking columns.
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS name VARCHAR(200)")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS module_type VARCHAR(50)")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS action_type VARCHAR(50)")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS variables TEXT")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS provider_template_id VARCHAR(100)")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS transition_id UUID REFERENCES workflow_transitions(id) ON DELETE SET NULL")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_notification_templates_module_type ON notification_templates(module_type)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_notification_templates_action_type ON notification_templates(action_type)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_notification_templates_transition_id ON notification_templates(transition_id)")

	// First Response Time is computed on read: the incident report query scans incident_revisions
	// per incident to find the earliest qualifying staff action. This composite index serves that
	// DISTINCT ON directly; without it the query degrades to a full scan as revisions accumulate.
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_incident_revisions_frt ON incident_revisions (incident_id, created_at, revision_number) INCLUDE (action_type, performed_by_id)`)

	// Bilingual redesign: add new columns, migrate existing data, drop old single-language columns.
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS subject_en TEXT")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS body_en    TEXT")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS subject_ar TEXT")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS body_ar    TEXT")
	// The legacy single-language column only exists until this migration has run once.
	if db.Migrator().HasColumn("notification_templates", "language") {
		db.Exec("UPDATE notification_templates SET subject_en = subject, body_en = body WHERE language = 'en' AND body_en IS NULL")
		db.Exec(`UPDATE notification_templates t SET subject_ar = ar.subject, body_ar = ar.body FROM notification_templates ar WHERE ar.language = 'ar' AND ar.code = t.code AND ar.channel = t.channel AND t.language = 'en'`)
		db.Exec("DELETE FROM notification_templates WHERE language = 'ar'")
	}
	db.Exec("ALTER TABLE notification_templates DROP COLUMN IF EXISTS language")
	db.Exec("ALTER TABLE notification_templates DROP COLUMN IF EXISTS subject")
	db.Exec("ALTER TABLE notification_templates DROP COLUMN IF EXISTS body")
	db.Exec("ALTER TABLE notification_templates DROP COLUMN IF EXISTS description")
	// Enforce uniqueness of (code, channel) for non-deleted templates so duplicate codes
	// are rejected at the DB level in addition to the service-level check.
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_notification_templates_code_channel ON notification_templates(code, channel) WHERE deleted_at IS NULL`)

	// Workflow code column was previously VARCHAR(50) which is too short for some existing workflows. Increase to 100 chars. Note: MySQL syntax is ALTER TABLE workflows MODIFY code VARCHAR(100) NOT NULL but Postgres is ALTER TABLE workflows ALTER COLUMN code TYPE VARCHAR(100). GORM's AutoMigrate doesn't handle this edge case, so we run raw SQL. Idempotent: safe to run repeatedly.
	db.Exec("ALTER TABLE workflows ALTER COLUMN code TYPE VARCHAR(100)")
	db.Exec("ALTER TABLE reports ALTER COLUMN timestamp_key TYPE VARCHAR(100)")

	// Master data name/name_ar widened from 100 to 500 chars (classifications, locations, departments).
	for _, t := range []string{"classifications", "locations", "departments"} {
		db.Exec("ALTER TABLE " + t + " ALTER COLUMN name TYPE VARCHAR(500)")
		db.Exec("ALTER TABLE " + t + " ALTER COLUMN name_ar TYPE VARCHAR(500)")
	}
	db.Exec("ALTER TABLE reports ALTER COLUMN timestamp_key SET DEFAULT 'created_at'")

	// Error Log: sequence for ERR-000123 style codes
	if err := migrations.MigrateErrorLogSequence(db); err != nil {
		return fmt.Errorf("failed to create error_log_seq: %w", err)
	}

	// Migrate transition assignment roles (single → many-to-many)
	if err := migrations.MigrateTransitionAssignmentRoles(db); err != nil {
		log.Printf("Warning: transition assignment roles migration failed: %v", err)
	}

	// Migrate state assignment roles (creation-time user assignment join table)
	if err := migrations.MigrateStateAssignmentRoles(db); err != nil {
		log.Printf("Warning: state assignment roles migration failed: %v", err)
	}

	// Migrate closed incident edit tracking
	if err := migrations.MigrateClosedIncidentEditTracking(db); err != nil {
		log.Printf("Warning: closed incident edit tracking migration failed: %v", err)
	}

	// Migrate user mobile verified
	if err := migrations.MigrateUserMobileVerified(db); err != nil {
		log.Printf("Warning: user mobile verified migration failed: %v", err)
	}

	// Migrate workflow state lock_assignee column
	if err := migrations.MigrateLockAssignee(db); err != nil {
		log.Printf("Warning: lock assignee migration failed: %v", err)
	}

	// Migrate classification single-type column → classification_types join table
	if err := migrations.MigrateClassificationTypes(db); err != nil {
		log.Printf("Warning: classification types migration failed: %v", err)
	}

	// Migrate user national_id column
	if err := migrations.MigrateUserNationalID(db); err != nil {
		log.Printf("Warning: user national_id migration failed: %v", err)
	}

	// Enforce phone uniqueness at the DB level — phone stays optional, but a
	// non-blank number may only belong to one live user
	if err := migrations.MigrateUserPhoneUnique(db); err != nil {
		log.Printf("Warning: user phone unique index migration failed: %v", err)
	}

	// Backfill Arabic name/description/module on permissions. The Seed loop below
	// only inserts missing permissions, so existing rows would otherwise never get
	// translations. Idempotent and self-skipping once nothing is blank.
	if err := migrations.MigratePermissionArabic(db); err != nil {
		log.Printf("Warning: permission Arabic backfill failed: %v", err)
	}

	// Composite index for the incident communication history query (filter by incident_id, sort by created_at)
	if err := migrations.MigrateNotificationLogIncidentIndex(db); err != nil {
		log.Printf("Warning: notification_log incident index migration failed: %v", err)
	}

	// Default SMS templates replacing formerly hardcoded SMS text (idempotent, never overwrites edits).
	// can remove after successfull templates migartion.
	if err := migrations.SeedDefaultSMSTemplates(db); err != nil {
		log.Printf("Warning: default SMS template seed failed: %v", err)
	}

	// Assign a unique Organization Code (ORG-######) to any department missing one.
	// EPM940 only — other clients supply department codes in the payload. Idempotent:
	// only touches rows with an empty/NULL code.
	if strings.EqualFold(strings.TrimSpace(cfg.ClientCode), constants.CLIENT_CODE.EPM940) {
		if err := migrations.MigrateDepartmentCodeBackfill(db); err != nil {
			log.Printf("Warning: department code backfill failed: %v", err)
		}
		if err := migrations.MigrateClassificationCodeBackfill(db); err != nil {
			log.Printf("Warning: classification code backfill failed: %v", err)
		}
		if err := migrations.MigrateRoleCodeBackfill(db); err != nil {
			log.Printf("Warning: role code backfill failed: %v", err)
		}
		if err := migrations.MigrateLocationCodeBackfill(db); err != nil {
			log.Printf("Warning: location code backfill failed: %v", err)
		}
	}

	// Partial Close feature columns — idempotent, safe to run repeatedly
	db.Exec("ALTER TABLE workflow_states ADD COLUMN IF NOT EXISTS is_partial_close BOOLEAN NOT NULL DEFAULT false")
	db.Exec("ALTER TABLE incidents ADD COLUMN IF NOT EXISTS partial_close_expires_at TIMESTAMPTZ")
	db.Exec("ALTER TABLE incidents ADD COLUMN IF NOT EXISTS partial_close_duration VARCHAR(100) NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE incidents ADD COLUMN IF NOT EXISTS partial_close_notified BOOLEAN NOT NULL DEFAULT false")

	// AD/LDAP user flag — idempotent
	db.Exec("ALTER TABLE users ADD COLUMN IF NOT EXISTS is_ad_user BOOLEAN NOT NULL DEFAULT false")

	// role_permissions is a GORM many2many join table with no primary key.
	// Without a replica identity PostgreSQL refuses DELETE operations on tables
	// that are part of a logical replication publication (SQLSTATE 55000).
	// FULL uses the entire row as identity — idempotent, safe to run repeatedly.
	db.Exec("ALTER TABLE role_permissions REPLICA IDENTITY FULL")

	log.Println("Database migrations completed")
	return nil
}

func Seed(db *gorm.DB, cfg *config.Config) error {
	log.Println("Seeding database...")

	// Seed default permissions
	permissions := []models.Permission{
		// User permissions
		{Name: "View Users", Code: "users:view", Module: "users", Action: "view", ActionAr: "عرض", Description: "View user list and details"},
		{Name: "Create Users", Code: "users:create", Module: "users", Action: "create", ActionAr: "إنشاء", Description: "Create new users"},
		{Name: "Update Users", Code: "users:update", Module: "users", Action: "update", ActionAr: "تحديث", Description: "Update user information"},
		{Name: "Delete Users", Code: "users:delete", Module: "users", Action: "delete", ActionAr: "حذف", Description: "Delete users"},
		{Name: "Reset User Password", Code: "users:reset_password", Module: "users", Action: "reset_password", ActionAr: "إعادة تعيين كلمة المرور", Description: "Reset user password"},

		// Role permissions
		{Name: "View Roles", Code: "roles:view", Module: "roles", Action: "view", ActionAr: "عرض", Description: "View roles list"},
		{Name: "Create Roles", Code: "roles:create", Module: "roles", Action: "create", ActionAr: "إنشاء", Description: "Create new roles"},
		{Name: "Update Roles", Code: "roles:update", Module: "roles", Action: "update", ActionAr: "تحديث", Description: "Update roles"},
		{Name: "Delete Roles", Code: "roles:delete", Module: "roles", Action: "delete", ActionAr: "حذف", Description: "Delete roles"},

		// Permission management
		{Name: "View Permissions", Code: "permissions:view", Module: "permissions", Action: "view", ActionAr: "عرض", Description: "View permissions"},
		{Name: "Manage Permissions", Code: "permissions:manage", Module: "permissions", Action: "manage", ActionAr: "إدارة", Description: "Manage permissions"},

		// Department permissions
		{Name: "View Departments", Code: "departments:view", Module: "departments", Action: "view", ActionAr: "عرض", Description: "View departments"},
		{Name: "Create Departments", Code: "departments:create", Module: "departments", Action: "create", ActionAr: "إنشاء", Description: "Create departments"},
		{Name: "Update Departments", Code: "departments:update", Module: "departments", Action: "update", ActionAr: "تحديث", Description: "Update departments"},
		{Name: "Delete Departments", Code: "departments:delete", Module: "departments", Action: "delete", ActionAr: "حذف", Description: "Delete departments"},

		// Location permissions
		{Name: "View Locations", Code: "locations:view", Module: "locations", Action: "view", ActionAr: "عرض", Description: "View locations"},
		{Name: "Create Locations", Code: "locations:create", Module: "locations", Action: "create", ActionAr: "إنشاء", Description: "Create locations"},
		{Name: "Update Locations", Code: "locations:update", Module: "locations", Action: "update", ActionAr: "تحديث", Description: "Update locations"},
		{Name: "Delete Locations", Code: "locations:delete", Module: "locations", Action: "delete", ActionAr: "حذف", Description: "Delete locations"},

		// Classification permissions
		{Name: "View Classifications", Code: "classifications:view", Module: "classifications", Action: "view", ActionAr: "عرض", Description: "View classifications"},
		{Name: "Create Classifications", Code: "classifications:create", Module: "classifications", Action: "create", ActionAr: "إنشاء", Description: "Create classifications"},
		{Name: "Update Classifications", Code: "classifications:update", Module: "classifications", Action: "update", ActionAr: "تحديث", Description: "Update classifications"},
		{Name: "Delete Classifications", Code: "classifications:delete", Module: "classifications", Action: "delete", ActionAr: "حذف", Description: "Delete classifications"},

		// Category permissions (hierarchical taxonomy used by goals)

		// Settings permissions
		{Name: "View Settings", Code: "settings:view", Module: "settings", Action: "view", ActionAr: "عرض", Description: "View system settings"},
		{Name: "Update Settings", Code: "settings:update", Module: "settings", Action: "update", ActionAr: "تحديث", Description: "Update system settings"},

		// Workflow permissions
		{Name: "View Workflows", Code: "workflows:view", Module: "workflows", Action: "view", ActionAr: "عرض", Description: "View workflow templates"},
		{Name: "Create Workflows", Code: "workflows:create", Module: "workflows", Action: "create", ActionAr: "إنشاء", Description: "Create workflow templates"},
		{Name: "Update Workflows", Code: "workflows:update", Module: "workflows", Action: "update", ActionAr: "تحديث", Description: "Update workflow templates"},
		{Name: "Delete Workflows", Code: "workflows:delete", Module: "workflows", Action: "delete", ActionAr: "حذف", Description: "Delete workflow templates"},
		{Name: "Design Workflows", Code: "workflows:design", Module: "workflows", Action: "design", ActionAr: "تصميم", Description: "Access workflow designer"},

		// Incident permissions
		{Name: "View Incidents", Code: "incidents:view", Module: "incidents", Action: "view", ActionAr: "عرض", Description: "View incidents"},
		{Name: "Create Incidents", Code: "incidents:create", Module: "incidents", Action: "create", ActionAr: "إنشاء", Description: "Create new incidents"},
		{Name: "Update Incidents", Code: "incidents:update", Module: "incidents", Action: "update", ActionAr: "تحديث", Description: "Update incident fields"},
		{Name: "Delete Incidents", Code: "incidents:delete", Module: "incidents", Action: "delete", ActionAr: "حذف", Description: "Delete incidents"},
		{Name: "Transition Incidents", Code: "incidents:transition", Module: "incidents", Action: "transition", ActionAr: "انتقال الحالة", Description: "Execute state transitions"},
		{Name: "Assign Incidents", Code: "incidents:assign", Module: "incidents", Action: "assign", ActionAr: "تعيين", Description: "Assign/reassign incidents"},
		{Name: "Comment on Incidents", Code: "incidents:comment", Module: "incidents", Action: "comment", ActionAr: "تعليق", Description: "Add comments to incidents"},
		{Name: "View All Incidents", Code: "incidents:view_all", Module: "incidents", Action: "view_all", ActionAr: "عرض الكل", Description: "View all incidents regardless of assignment"},
		{Name: "Manage SLA", Code: "incidents:manage_sla", Module: "incidents", Action: "manage_sla", ActionAr: "إدارة اتفاقية مستوى الخدمة", Description: "Override SLA settings"},
		{Name: "Merge Incidents", Code: "incidents:merge", Module: "incidents", Action: "merge", ActionAr: "دمج", Description: "Merge multiple incidents into one"},
		{Name: "Edit Closed Incidents", Code: "incidents:edit-closed", Module: "incidents", Action: "edit_closed", ActionAr: "تعديل المغلق", Description: "Edit summary/description of closed incidents"},
		{Name: "Request Info on Incidents", Code: "incidents:request-info", Module: "incidents", Action: "request_info", ActionAr: "طلب معلومات", Description: "Request additional information from citizens"},
		{Name: "Share Incidents", Code: "incidents:share", Module: "incidents", Action: "share", ActionAr: "مشاركة", Description: "Share incident details with external parties"},
		{Name: "Filter Incidents by Reporter Phone", Code: "incidents:filter_reporter_phone", Module: "incidents", Action: "filter_reporter_phone", ActionAr: "تصفية حسب هاتف المبلغ", Description: "Filter incidents by reporter phone number"},
		{Name: "Upload Attachment Gallery", Code: "incidents:upload-attachment-gallery", Module: "incidents", Action: "upload_attachment_gallery", ActionAr: "رفع معرض المرفقات", Description: "Upload attachments to the incident attachment gallery"},

		// Request permissions
		{Name: "View Requests", Code: "requests:view", Module: "requests", Action: "view", ActionAr: "عرض", Description: "View requests"},
		{Name: "Create Requests", Code: "requests:create", Module: "requests", Action: "create", ActionAr: "إنشاء", Description: "Create new requests"},
		{Name: "Update Requests", Code: "requests:update", Module: "requests", Action: "update", ActionAr: "تحديث", Description: "Update request fields"},
		{Name: "Delete Requests", Code: "requests:delete", Module: "requests", Action: "delete", ActionAr: "حذف", Description: "Delete requests"},
		{Name: "Transition Requests", Code: "requests:transition", Module: "requests", Action: "transition", ActionAr: "انتقال الحالة", Description: "Execute request state transitions"},
		{Name: "Assign Requests", Code: "requests:assign", Module: "requests", Action: "assign", ActionAr: "تعيين", Description: "Assign/reassign requests"},
		{Name: "Comment on Requests", Code: "requests:comment", Module: "requests", Action: "comment", ActionAr: "تعليق", Description: "Add comments to requests"},
		{Name: "View All Requests", Code: "requests:view_all", Module: "requests", Action: "view_all", ActionAr: "عرض الكل", Description: "View all requests regardless of assignment"},

		// Complaint permissions
		{Name: "View Complaints", Code: "complaints:view", Module: "complaints", Action: "view", ActionAr: "عرض", Description: "View complaints"},
		{Name: "Create Complaints", Code: "complaints:create", Module: "complaints", Action: "create", ActionAr: "إنشاء", Description: "Create new complaints"},
		{Name: "Update Complaints", Code: "complaints:update", Module: "complaints", Action: "update", ActionAr: "تحديث", Description: "Update complaint fields"},
		{Name: "Delete Complaints", Code: "complaints:delete", Module: "complaints", Action: "delete", ActionAr: "حذف", Description: "Delete complaints"},
		{Name: "Transition Complaints", Code: "complaints:transition", Module: "complaints", Action: "transition", ActionAr: "انتقال الحالة", Description: "Execute complaint state transitions"},
		{Name: "Assign Complaints", Code: "complaints:assign", Module: "complaints", Action: "assign", ActionAr: "تعيين", Description: "Assign/reassign complaints"},
		{Name: "Comment on Complaints", Code: "complaints:comment", Module: "complaints", Action: "comment", ActionAr: "تعليق", Description: "Add comments to complaints"},
		{Name: "View All Complaints", Code: "complaints:view_all", Module: "complaints", Action: "view_all", ActionAr: "عرض الكل", Description: "View all complaints regardless of assignment"},

		// Query permissions
		{Name: "View Queries", Code: "queries:view", Module: "queries", Action: "view", ActionAr: "عرض", Description: "View queries"},
		{Name: "Create Queries", Code: "queries:create", Module: "queries", Action: "create", ActionAr: "إنشاء", Description: "Create new queries"},
		{Name: "Update Queries", Code: "queries:update", Module: "queries", Action: "update", ActionAr: "تحديث", Description: "Update query fields"},
		{Name: "Delete Queries", Code: "queries:delete", Module: "queries", Action: "delete", ActionAr: "حذف", Description: "Delete queries"},
		{Name: "Transition Queries", Code: "queries:transition", Module: "queries", Action: "transition", ActionAr: "انتقال الحالة", Description: "Execute query state transitions"},
		{Name: "Assign Queries", Code: "queries:assign", Module: "queries", Action: "assign", ActionAr: "تعيين", Description: "Assign/reassign queries"},
		{Name: "Comment on Queries", Code: "queries:comment", Module: "queries", Action: "comment", ActionAr: "تعليق", Description: "Add comments to queries"},
		{Name: "View All Queries", Code: "queries:view_all", Module: "queries", Action: "view_all", ActionAr: "عرض الكل", Description: "View all queries regardless of assignment"},

		// Report permissions
		{Name: "View Reports", Code: "reports:view", Module: "reports", Action: "view", ActionAr: "عرض", Description: "View reports"},
		{Name: "Create Reports", Code: "reports:create", Module: "reports", Action: "create", ActionAr: "إنشاء", Description: "Create new reports"},
		{Name: "Update Reports", Code: "reports:update", Module: "reports", Action: "update", ActionAr: "تحديث", Description: "Update reports"},
		{Name: "Delete Reports", Code: "reports:delete", Module: "reports", Action: "delete", ActionAr: "حذف", Description: "Delete reports"},

		// Action Log permissions
		{Name: "View Action Logs", Code: "action-logs:view", Module: "action-logs", Action: "view", ActionAr: "عرض", Description: "View action logs"},
		{Name: "Delete Action Logs", Code: "action-logs:delete", Module: "action-logs", Action: "delete", ActionAr: "حذف", Description: "Delete/cleanup action logs"},

		// Lookup permissions
		{Name: "View Lookups", Code: "lookups:view", Module: "lookups", Action: "view", ActionAr: "عرض", Description: "View lookup categories and values"},
		{Name: "Create Lookups", Code: "lookups:create", Module: "lookups", Action: "create", ActionAr: "إنشاء", Description: "Create lookup categories and values"},
		{Name: "Update Lookups", Code: "lookups:update", Module: "lookups", Action: "update", ActionAr: "تحديث", Description: "Update lookup categories and values"},
		{Name: "Delete Lookups", Code: "lookups:delete", Module: "lookups", Action: "delete", ActionAr: "حذف", Description: "Delete lookup categories and values"},

		// Application Links permissions
		{Name: "View Application Links", Code: "application-links:view", Module: "application-links", Action: "view", ActionAr: "عرض", Description: "View application links"},
		{Name: "Create Application Links", Code: "application-links:create", Module: "application-links", Action: "create", ActionAr: "إنشاء", Description: "Create application links"},
		{Name: "Update Application Links", Code: "application-links:update", Module: "application-links", Action: "update", ActionAr: "تحديث", Description: "Update application links"},
		{Name: "Delete Application Links", Code: "application-links:delete", Module: "application-links", Action: "delete", ActionAr: "حذف", Description: "Delete application links"},
		{Name: "Access Application Links on Dashboard", Code: "application-links:dashboard", Module: "application-links", Action: "dashboard", ActionAr: "لوحة التحكم", Description: "See and launch application links from the dashboard"},

		// Config permissions
		{Name: "View Configs", Code: "configs:view", Module: "configs", Action: "view", ActionAr: "عرض", Description: "View environment configs"},
		{Name: "Create Configs", Code: "configs:create", Module: "configs", Action: "create", ActionAr: "إنشاء", Description: "Create environment configs"},
		{Name: "Update Configs", Code: "configs:update", Module: "configs", Action: "update", ActionAr: "تحديث", Description: "Update environment configs"},
		{Name: "Delete Configs", Code: "configs:delete", Module: "configs", Action: "delete", ActionAr: "حذف", Description: "Delete environment configs"},

		// Notification permissions
		{Name: "View Notifications", Code: "notifications:read", Module: "notifications", Action: "read", ActionAr: "قراءة", Description: "View notification logs"},
		{Name: "Send Notifications", Code: "notifications:send", Module: "notifications", Action: "send", ActionAr: "إرسال", Description: "Send email/SMS notifications"},
		{Name: "Create Draft Notifications", Code: "notifications:create", Module: "notifications", Action: "create", ActionAr: "إنشاء", Description: "Create draft notifications"},
		{Name: "Update Draft Notifications", Code: "notifications:update", Module: "notifications", Action: "update", ActionAr: "تحديث", Description: "Update draft notifications"},
		{Name: "Delete Notifications", Code: "notifications:delete", Module: "notifications", Action: "delete", ActionAr: "حذف", Description: "Delete notification logs"},

		// Communication Tracking Dashboard permissions (Call Center module)
		{Name: "View Communication Tracking Dashboard", Code: "communication-tracking:view", Module: "communication-tracking", Action: "view", ActionAr: "عرض", Description: "View the cross-channel communication tracking dashboard (SMS/Email/WhatsApp delivery status)"},
		{Name: "Update Communication Tracking", Code: "communication-tracking:update", Module: "communication-tracking", Action: "update", ActionAr: "تحديث", Description: "Manually resend a failed/undeliverable/expired SMS, Email, or WhatsApp notification"},

		// Template permissions
		{Name: "View Templates", Code: "templates:read", Module: "templates", Action: "read", ActionAr: "قراءة", Description: "View notification templates"},
		{Name: "Create Templates", Code: "templates:create", Module: "templates", Action: "create", ActionAr: "إنشاء", Description: "Create notification templates"},
		{Name: "Update Templates", Code: "templates:update", Module: "templates", Action: "update", ActionAr: "تحديث", Description: "Update notification templates"},
		{Name: "Delete Templates", Code: "templates:delete", Module: "templates", Action: "delete", ActionAr: "حذف", Description: "Delete notification templates"},

		// Escalation Group permissions
		{Name: "View Escalation Groups", Code: "escalation-groups:view", Module: "escalation-groups", Action: "view", ActionAr: "عرض", Description: "View escalation groups list and details"},
		{Name: "Create Escalation Group", Code: "escalation-groups:create", Module: "escalation-groups", Action: "create", ActionAr: "إنشاء", Description: "Create new escalation groups"},
		{Name: "Update Escalation Group", Code: "escalation-groups:update", Module: "escalation-groups", Action: "update", ActionAr: "تحديث", Description: "Update escalation group settings"},
		{Name: "Delete Escalation Group", Code: "escalation-groups:delete", Module: "escalation-groups", Action: "delete", ActionAr: "حذف", Description: "Delete escalation groups"},
		{Name: "Assign Users to Escalation Group", Code: "escalation-groups:assign_users", Module: "escalation-groups", Action: "assign_users", ActionAr: "تعيين المستخدمين", Description: "Add or remove users from escalation groups"},
		{Name: "Manage Escalation Rules", Code: "escalation-groups:manage_rules", Module: "escalation-groups", Action: "manage_rules", ActionAr: "إدارة القواعد", Description: "Configure escalation frequency, channel, and classification rules"},

		// Escalation log permissions
		{Name: "View Escalation Log", Code: "escalation-logs:view", Module: "escalation-logs", Action: "view", ActionAr: "عرض", Description: "View the escalation log and per-report escalation history"},
		{Name: "Export Escalation Log", Code: "escalation-logs:export", Module: "escalation-logs", Action: "export", ActionAr: "تصدير", Description: "Export the filtered escalation log to Excel"},

		// Error Log permissions
		{Name: "View Error Logs", Code: "error-logs:view", Module: "error-logs", Action: "view", ActionAr: "عرض", Description: "View the error log list and details"},
		{Name: "Update Error Log Status", Code: "error-logs:update", Module: "error-logs", Action: "update", ActionAr: "تحديث", Description: "Change error status and add notes"},
		{Name: "Export Error Logs", Code: "error-logs:export", Module: "error-logs", Action: "export", ActionAr: "تصدير", Description: "Export the filtered error log list"},
		{Name: "View Error Technical Details", Code: "error-logs:view_technical", Module: "error-logs", Action: "view_technical", ActionAr: "عرض التفاصيل الفنية", Description: "View technical details of error log entries"},

		// Escalation Policy permissions
		{Name: "Create Escalation Policy", Code: "escalation-policies:create", Module: "escalation-policies", Action: "create", ActionAr: "إنشاء", Description: "Create new escalation policies"},

		// License permissions
		{Name: "View License", Code: "license:view", Module: "license", Action: "view", ActionAr: "عرض", Description: "View license status and info"},
		{Name: "Manage License", Code: "license:manage", Module: "license", Action: "manage", ActionAr: "إدارة", Description: "Activate, deactivate, and manage license keys"},

		// Dashboard permissions
		{Name: "Admin Dashboard", Code: "dashboard:admin", Module: "dashboard", Action: "admin", ActionAr: "مسؤول", Description: "Access admin section cards on dashboard"},
		{Name: "Incidents Dashboard", Code: "dashboard:incidents", Module: "dashboard", Action: "incidents", ActionAr: "البلاغات", Description: "Access incident cards on dashboard"},
		{Name: "Requests Dashboard", Code: "dashboard:requests", Module: "dashboard", Action: "requests", ActionAr: "الطلبات", Description: "Access request cards on dashboard"},
		{Name: "Complaints Dashboard", Code: "dashboard:complaints", Module: "dashboard", Action: "complaints", ActionAr: "الشكاوى", Description: "Access complaint cards on dashboard"},
		{Name: "Queries Dashboard", Code: "dashboard:queries", Module: "dashboard", Action: "queries", ActionAr: "الاستفسارات", Description: "Access query cards on dashboard"},
		{Name: "Workflows Dashboard", Code: "dashboard:workflows", Module: "dashboard", Action: "workflows", ActionAr: "سير العمل", Description: "Access workflow cards on dashboard"},
		{Name: "CCM Dashboard", Code: "dashboard:ccm", Module: "dashboard", Action: "ccm", ActionAr: "إدارة علاقات المواطنين", Description: "Access ccm cards on dashboard"},

		// Department-scoped permissions (configurable scoping)
		{Name: "View Only Department Incidents", Code: "incidents:view_department_only", Module: "incidents", Action: "view_department_only", ActionAr: "عرض القسم فقط", Description: "Restrict incident view to own department"},
		{Name: "View Only Department Users", Code: "users:view_department_only", Module: "users", Action: "view_department_only", ActionAr: "عرض القسم فقط", Description: "Restrict user view to own department"},
	}

	for _, perm := range permissions {
		var existing models.Permission
		result := db.Where("code = ?", perm.Code).First(&existing)
		if result.Error == gorm.ErrRecordNotFound {
			if err := db.Create(&perm).Error; err != nil {
				log.Printf("Failed to create permission %s: %v", perm.Code, err)
			}
		}
	}

	// Seed default roles
	var allPerms []models.Permission
	db.Find(&allPerms)

	// Admin role gets all permissions EXCEPT department-scoping restrictions.
	// "view_department_only" permissions are restrictive (they limit a user to their
	// own department) and should only be on department-scoped roles like Department
	// Manager and Supervisor — not on the Administrator role.
	var adminPerms []models.Permission
	for _, p := range allPerms {
		if !strings.HasSuffix(p.Code, "view_department_only") {
			adminPerms = append(adminPerms, p)
		}
	}

	var adminRole models.Role
	result := db.Where("code = ?", "admin").First(&adminRole)
	if result.Error == gorm.ErrRecordNotFound {
		adminRole = models.Role{
			Name:        "Administrator",
			Code:        "admin",
			Description: "Full system access",
			IsSystem:    true,
			IsActive:    true,
			Permissions: adminPerms,
		}
		db.Create(&adminRole)
	} else if db.Model(&adminRole).Association("Permissions").Count() == 0 {
		// Only seed permissions if role has none (fresh DB); skip if already configured
		db.Model(&adminRole).Association("Permissions").Append(adminPerms)
	}

	// Ensure the admin role has the Error Log permissions on existing databases.
	var errorLogPerms []models.Permission
	db.Where("module = ?", "error-logs").Find(&errorLogPerms)
	if len(errorLogPerms) > 0 {
		db.Model(&adminRole).Association("Permissions").Append(errorLogPerms)
	}

	// Ensure the admin role has the Escalation Log permissions on existing databases.
	var escalationLogPerms []models.Permission
	db.Where("module = ?", "escalation-logs").Find(&escalationLogPerms)
	if len(escalationLogPerms) > 0 {
		db.Model(&adminRole).Association("Permissions").Append(escalationLogPerms)
	}

	// Remove department-scoping permissions from admin role on existing databases.
	// These are restrictive and should never be on the Administrator role.
	var deptScopePerms []models.Permission
	db.Where("code LIKE ?", "%view_department_only").Find(&deptScopePerms)
	if len(deptScopePerms) > 0 {
		db.Model(&adminRole).Association("Permissions").Delete(deptScopePerms)
	}

	// User role with basic permissions
	var userRole models.Role
	result = db.Where("code = ?", "user").First(&userRole)
	if result.Error == gorm.ErrRecordNotFound {
		var viewPerms []models.Permission
		db.Where("action = ? AND module <> ?", "view", "error-logs").Find(&viewPerms) // Error Log is admin/support only
		userRole = models.Role{
			Name:        "User",
			Code:        "user",
			Description: "Basic user access",
			IsSystem:    true,
			IsActive:    true,
			Permissions: viewPerms,
		}
		db.Create(&userRole)
	} else if db.Model(&userRole).Association("Permissions").Count() == 0 {
		var viewPerms []models.Permission
		db.Where("action = ? AND module <> ?", "view", "error-logs").Find(&viewPerms) // Error Log is admin/support only
		db.Model(&userRole).Association("Permissions").Append(viewPerms)
	}

	// Manager role with broader permissions
	var managerRole models.Role
	result = db.Where("code = ?", "manager").First(&managerRole)
	if result.Error == gorm.ErrRecordNotFound {
		var managerPerms []models.Permission
		db.Where("action IN ?", []string{"view", "create", "update", "delete", "assign", "approve"}).Find(&managerPerms)
		// targets:reject uses action "reject" (mirroring perf:reject), which
		// isn't in the action list above — without this, splitting Reject out
		// of targets:approve into its own permission would silently take away
		// Manager's existing ability to reject a submitted target.
		var managerRejectPerm models.Permission
		if db.Where("code = ?", "targets:reject").First(&managerRejectPerm).Error == nil {
			managerPerms = append(managerPerms, managerRejectPerm)
		}
		managerRole = models.Role{
			Name:        "Manager",
			Code:        "manager",
			Description: "Department manager access",
			IsSystem:    true,
			IsActive:    true,
			Permissions: managerPerms,
		}
		db.Create(&managerRole)
	} else if db.Model(&managerRole).Association("Permissions").Count() == 0 {
		var managerPerms []models.Permission
		db.Where("action IN ?", []string{"view", "create", "update", "delete", "assign", "approve"}).Find(&managerPerms)
		var managerRejectPerm models.Permission
		if db.Where("code = ?", "targets:reject").First(&managerRejectPerm).Error == nil {
			managerPerms = append(managerPerms, managerRejectPerm)
		}
		db.Model(&managerRole).Association("Permissions").Replace(managerPerms)
	}

	// Department Manager role — manages users within assigned scope
	var deptManagerRole models.Role
	result = db.Where("code = ?", "department_manager").First(&deptManagerRole)
	if result.Error == gorm.ErrRecordNotFound {
		var deptManagerPerms []models.Permission
		db.Where("code IN ?", []string{
			"users:view", "users:create", "users:update",
			"incidents:view", "incidents:view_all", "incidents:transition", "incidents:assign", "incidents:comment",
			"requests:view", "requests:view_all", "requests:transition", "requests:assign", "requests:comment",
			"complaints:view", "complaints:view_all", "complaints:transition", "complaints:assign", "complaints:comment",
			"queries:view", "queries:view_all", "queries:transition", "queries:assign", "queries:comment",
			"incidents:view_department_only", "users:view_department_only",
		}).Find(&deptManagerPerms)
		deptManagerRole = models.Role{
			Name:                "Department Manager",
			Code:                "department_manager",
			Description:         "Department manager with restricted scope to assigned department, classification, and location",
			IsSystem:            true,
			IsActive:            true,
			IsDepartmentManager: true,
			Permissions:         deptManagerPerms,
		}
		db.Create(&deptManagerRole)
	} else {
		if db.Model(&deptManagerRole).Association("Permissions").Count() == 0 {
			var deptManagerPerms []models.Permission
			db.Where("code IN ?", []string{
				"users:view", "users:create", "users:update",
				"incidents:view", "incidents:view_all", "incidents:transition", "incidents:assign", "incidents:comment",
				"requests:view", "requests:view_all", "requests:transition", "requests:assign", "requests:comment",
				"complaints:view", "complaints:view_all", "complaints:transition", "complaints:assign", "complaints:comment",
				"queries:view", "queries:view_all", "queries:transition", "queries:assign", "queries:comment",
				"incidents:view_department_only", "users:view_department_only",
			}).Find(&deptManagerPerms)
			db.Model(&deptManagerRole).Association("Permissions").Append(deptManagerPerms)
		}
		if !deptManagerRole.IsDepartmentManager {
			db.Model(&deptManagerRole).Update("is_department_manager", true)
		}
	}

	// Supervisor role with view permissions for department scoping
	var supervisorRole models.Role
	result = db.Where("code = ?", "supervisor").First(&supervisorRole)
	if result.Error == gorm.ErrRecordNotFound {
		var supervisorPerms []models.Permission
		db.Where("code IN ?", []string{
			"incidents:view", "incidents:view_all", "incidents:transition", "incidents:assign", "incidents:comment",
			"requests:view", "requests:view_all", "requests:transition", "requests:assign", "requests:comment",
			"complaints:view", "complaints:view_all", "complaints:transition", "complaints:assign", "complaints:comment",
			"queries:view", "queries:view_all", "queries:transition", "queries:assign", "queries:comment",
			"users:view",
			"incidents:view_department_only", "users:view_department_only",
		}).Find(&supervisorPerms)
		supervisorRole = models.Role{
			Name:        "Supervisor",
			Code:        "supervisor",
			Description: "Department supervisor with scoped access to tickets and personnel",
			IsSystem:    true,
			IsActive:    true,
			Permissions: supervisorPerms,
		}
		db.Create(&supervisorRole)
	} else if db.Model(&supervisorRole).Association("Permissions").Count() == 0 {
		var supervisorPerms []models.Permission
		db.Where("code IN ?", []string{
			"incidents:view", "incidents:view_all", "incidents:transition", "incidents:assign", "incidents:comment",
			"requests:view", "requests:view_all", "requests:transition", "requests:assign", "requests:comment",
			"complaints:view", "complaints:view_all", "complaints:transition", "complaints:assign", "complaints:comment",
			"queries:view", "queries:view_all", "queries:transition", "queries:assign", "queries:comment",
			"users:view",
			"incidents:view_department_only", "users:view_department_only",
		}).Find(&supervisorPerms)
		db.Model(&supervisorRole).Association("Permissions").Append(supervisorPerms)
	}

	// Create default super admin user
	var adminUser models.User
	result = db.Where("email = ?", "admin@automax.com").First(&adminUser)
	if result.Error == gorm.ErrRecordNotFound {
		hashedPassword, _ := utils.HashPassword("admin123")
		adminUser = models.User{
			Email:        "admin@automax.com",
			Username:     "admin",
			Password:     hashedPassword,
			FirstName:    "Super",
			LastName:     "Admin",
			IsActive:     true,
			IsSuperAdmin: true,
		}
		db.Create(&adminUser)
		db.Model(&adminUser).Association("Roles").Append(&adminRole)
	}

	// Seed default lookup categories
	seedLookupCategories(db, cfg)

	log.Println("Database seeding completed")
	return nil
}

func seedLookupCategories(db *gorm.DB, cfg *config.Config) {
	// Priority category
	var priorityCategory models.LookupCategory
	result := db.Where("code = ?", "PRIORITY").First(&priorityCategory)
	if result.Error == gorm.ErrRecordNotFound {
		priorityCategory = models.LookupCategory{
			Code:        "PRIORITY",
			Name:        "Priority",
			NameAr:      "الأولوية",
			Description: "Incident priority levels",
			IsSystem:    true,
			IsActive:    true,
		}
		if err := db.Create(&priorityCategory).Error; err != nil {
			log.Printf("Failed to create priority category: %v", err)
		} else {
			// Create priority values
			priorityValues := []models.LookupValue{
				{CategoryID: priorityCategory.ID, Code: "CRITICAL", Name: "Critical", NameAr: "حرج", SortOrder: 1, Color: "#EF4444", IsDefault: false, IsActive: true},
				{CategoryID: priorityCategory.ID, Code: "HIGH", Name: "High", NameAr: "عالي", SortOrder: 2, Color: "#F97316", IsDefault: false, IsActive: true},
				{CategoryID: priorityCategory.ID, Code: "MEDIUM", Name: "Medium", NameAr: "متوسط", SortOrder: 3, Color: "#EAB308", IsDefault: true, IsActive: true},
				{CategoryID: priorityCategory.ID, Code: "LOW", Name: "Low", NameAr: "منخفض", SortOrder: 4, Color: "#3B82F6", IsDefault: false, IsActive: true},
				{CategoryID: priorityCategory.ID, Code: "VERY_LOW", Name: "Very Low", NameAr: "منخفض جداً", SortOrder: 5, Color: "#6B7280", IsDefault: false, IsActive: true},
			}
			for _, v := range priorityValues {
				if err := db.Create(&v).Error; err != nil {
					log.Printf("Failed to create priority value %s: %v", v.Code, err)
				}
			}
		}
	}

	// Environment configuration category — EPM940 only
	if strings.EqualFold(strings.TrimSpace(cfg.ClientCode), constants.CLIENT_CODE.EPM940) {
		var categoryTypeCategory models.LookupCategory
		result = db.Where("code = ?", "ENV_CONFIGURATION").First(&categoryTypeCategory)
		if result.Error == gorm.ErrRecordNotFound {
			categoryTypeCategory = models.LookupCategory{
				Code:        "ENV_CONFIGURATION",
				Name:        "ENV CONFIGURATION",
				NameAr:      "إعدادات البيئة",
				Description: "System environment configuration",
				IsSystem:    true,
				IsActive:    true,
			}
			if err := db.Create(&categoryTypeCategory).Error; err != nil {
				log.Printf("Failed to create category type category: %v", err)
			} else {
				// Create category type values
				categoryTypeValues := []models.LookupValue{
					{CategoryID: categoryTypeCategory.ID, Code: "CITIZEN_ATTACHMENT_SIZE_LIMIT", Name: cfg.CitizenAttachmentSizeLimit, SortOrder: 1, Description: "Maximum size of citizen attachment", Color: "#EF4444", IsDefault: false, IsActive: true},
					{CategoryID: categoryTypeCategory.ID, Code: "INTERNAL_ATTACHMENT_SIZE_LIMIT", Name: cfg.InternalAttachmentSizeLimit, SortOrder: 2, Description: "Maximum size of internal attachment", Color: "#F97316", IsDefault: false, IsActive: true},
					{CategoryID: categoryTypeCategory.ID, Code: "MAX_DESCRIPTION_LENGTH", Name: strconv.Itoa(cfg.MaxDescriptionLength), SortOrder: 3, Description: "Maximum description length", Color: "#EAB308", IsDefault: false, IsActive: true},
					{CategoryID: categoryTypeCategory.ID, Code: "CHATBOT_URL", Name: cfg.ChatbotURL, SortOrder: 4, Description: "URL of the chatbot", Color: "#3B82F6", IsDefault: false, IsActive: true},
				}
				for _, v := range categoryTypeValues {
					if err := db.Create(&v).Error; err != nil {
						log.Printf("Failed to create category type value %s: %v", v.Code, err)
					}
				}
			}
		}
	}
}

// seedMetricApprovalWorkflows seeds two parallel workflows used by the
// metric approval gate (item #21):
//   - record_type='metric' covers initial metric creation visibility.
//   - record_type='metric_value_change' covers each subsequent value update.
func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
