CREATE TYPE "public"."connection_status" AS ENUM('PENDING', 'PROVISIONING', 'ACTIVE', 'PAUSED', 'ERROR');--> statement-breakpoint
CREATE TYPE "public"."role" AS ENUM('OWNER', 'ADMIN', 'MEMBER');--> statement-breakpoint
CREATE TYPE "public"."subscription_status" AS ENUM('INCOMPLETE', 'ACTIVE', 'PAST_DUE', 'CANCELED');--> statement-breakpoint
CREATE TYPE "public"."tier" AS ENUM('STARTER', 'ADVANCED', 'PRO');--> statement-breakpoint
CREATE TABLE "Connection" (
	"id" text PRIMARY KEY NOT NULL,
	"tenant_id" text NOT NULL,
	"subscription_id" text NOT NULL,
	"key" text NOT NULL,
	"label" text NOT NULL,
	"broker_server" text,
	"status" "connection_status" DEFAULT 'PENDING' NOT NULL,
	"relay_connection_id" text,
	"created_at" timestamp DEFAULT now() NOT NULL,
	"updated_at" timestamp DEFAULT now() NOT NULL,
	CONSTRAINT "Connection_key_unique" UNIQUE("key")
);
--> statement-breakpoint
CREATE TABLE "Plan" (
	"id" text PRIMARY KEY NOT NULL,
	"tier" "tier" NOT NULL,
	"name" text NOT NULL,
	"max_connections" integer NOT NULL,
	"monthly_price" integer NOT NULL,
	"stripe_price_id" text,
	"active" boolean DEFAULT true NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL,
	"updated_at" timestamp DEFAULT now() NOT NULL,
	CONSTRAINT "Plan_tier_unique" UNIQUE("tier")
);
--> statement-breakpoint
CREATE TABLE "Subscription" (
	"id" text PRIMARY KEY NOT NULL,
	"tenant_id" text NOT NULL,
	"plan_id" text NOT NULL,
	"status" "subscription_status" DEFAULT 'INCOMPLETE' NOT NULL,
	"current_period_end" timestamp,
	"stripe_subscription_id" text,
	"relay_subscription_id" text,
	"created_at" timestamp DEFAULT now() NOT NULL,
	"updated_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE TABLE "Tenant" (
	"id" text PRIMARY KEY NOT NULL,
	"name" text NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL,
	"updated_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE TABLE "User" (
	"id" text PRIMARY KEY NOT NULL,
	"email" text NOT NULL,
	"name" text,
	"role" "role" DEFAULT 'MEMBER' NOT NULL,
	"tenant_id" text NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL,
	"updated_at" timestamp DEFAULT now() NOT NULL,
	CONSTRAINT "User_email_unique" UNIQUE("email")
);
--> statement-breakpoint
ALTER TABLE "Connection" ADD CONSTRAINT "Connection_tenant_id_Tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "public"."Tenant"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "Connection" ADD CONSTRAINT "Connection_subscription_id_Subscription_id_fk" FOREIGN KEY ("subscription_id") REFERENCES "public"."Subscription"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "Subscription" ADD CONSTRAINT "Subscription_tenant_id_Tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "public"."Tenant"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "Subscription" ADD CONSTRAINT "Subscription_plan_id_Plan_id_fk" FOREIGN KEY ("plan_id") REFERENCES "public"."Plan"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "User" ADD CONSTRAINT "User_tenant_id_Tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "public"."Tenant"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "Connection_tenantId_idx" ON "Connection" USING btree ("tenant_id");--> statement-breakpoint
CREATE INDEX "Connection_subscriptionId_idx" ON "Connection" USING btree ("subscription_id");--> statement-breakpoint
CREATE INDEX "Subscription_tenantId_idx" ON "Subscription" USING btree ("tenant_id");--> statement-breakpoint
CREATE INDEX "Subscription_planId_idx" ON "Subscription" USING btree ("plan_id");--> statement-breakpoint
CREATE INDEX "User_tenantId_idx" ON "User" USING btree ("tenant_id");