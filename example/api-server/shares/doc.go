// Package shares implements the api-server example's share links. An
// authenticated POST /api/files/{id}/share creates a link with an optional
// expiry, and the public GET /api/shares/{token} returns the file's details
// and a presigned download link. The service registers its migrations and
// its email templates when it starts.
//
// Creating a share also queues a notification email on the queue named by
// the share_notification_queue setting. Queueing is best effort: a missing
// setting skips it and a queue error is logged, and the share is created
// either way. ProcessShareEmail sends the queued email from its template;
// the example does not wire it to a queue consumer.
package shares
