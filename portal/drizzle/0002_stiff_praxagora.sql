CREATE TABLE "BrokerSecret" (
	"connection_id" text PRIMARY KEY NOT NULL,
	"ciphertext" text NOT NULL,
	"iv" text NOT NULL,
	"auth_tag" text NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL,
	"updated_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
ALTER TABLE "BrokerSecret" ADD CONSTRAINT "BrokerSecret_connection_id_Connection_id_fk" FOREIGN KEY ("connection_id") REFERENCES "public"."Connection"("id") ON DELETE cascade ON UPDATE no action;