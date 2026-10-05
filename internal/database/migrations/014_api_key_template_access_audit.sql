ALTER TABLE api_credential_audit_events
DROP CONSTRAINT api_credential_audit_events_action_check;

ALTER TABLE api_credential_audit_events
ADD CONSTRAINT api_credential_audit_events_action_check
CHECK (action IN ('client.created', 'client.disabled', 'key.issued', 'key.rotated', 'key.revoked', 'key.template_access_updated'));
