// Package s3 is an S3 client that implements datastore.Backend. It uses only
// the standard library: net/http for transport, crypto/hmac for AWS Signature
// V4, encoding/xml for the few documents it reads.
//
// It relies on exactly this S3 behaviour and nothing else:
//
//   - single-part PUT (no multipart), and with If-None-Match: * an atomic
//     create-only write that answers 412 when the key exists;
//   - GET with a Range header, HEAD, and idempotent DELETE;
//   - ListObjectsV2 with prefix, continuation tokens, start-after and
//     url-encoded keys;
//   - read-after-write for new keys.
//
// Requests are signed with SigV4 (header form). The payload hash is
// UNSIGNED-PAYLOAD over TLS and the real SHA-256 over plain HTTP. Retries use
// jittered exponential backoff on 5xx, SlowDown, 408, 429 and connection
// errors; an If-None-Match write whose retry answers 412 is compared with what
// this client sent, so a lost response is not reported as a conflict. Path
// style and virtual-host style addressing are both supported, with an endpoint
// override and a region.
//
// A Backend owns one http.Transport with a connection pool sized for many
// parallel requests and large HTTP/2 flow-control windows. The caller provides
// the parallelism. Credentials live in a struct whose printed forms are
// redacted; they are never logged by this package.
package s3
