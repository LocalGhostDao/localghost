package debian

import "time"

// DeviceCertLife is how long a device certificate is good for: two weeks. Until 9 October 2026
// it was ten years, and a phone enrolled once held the door for a decade. Now a phone that is
// used renews its certificate every day it unlocks (secd's rekey, the phone's own key each
// time), like a cookie extended while it is in use, and a phone left for two weeks is a phone
// that has to be enrolled again with a fresh QR. The QR's own certificate lasts the same two
// weeks, so a QR drawn and never scanned, or photographed, is worth nothing after them.
const DeviceCertLife = 14 * 24 * time.Hour

// QRCertLife is how long the certificate drawn into the enrolment QR is good for: an hour. It is
// the one certificate whose private key has been outside the phone (on the terminal, in any
// photograph of it), and enrolment takes a minute: the phone scans, unlocks once with the PIN,
// and replaces it with a key of its own. After the hour a QR that was never scanned, or was
// photographed, reaches nothing; a phone that scanned it and did not unlock in time scans a
// fresh one.
const QRCertLife = time.Hour

// DeviceCertRenewAfter is the age at which a phone asks for a new certificate: a day. The box's
// certificates start an hour in the past (clock slack), so a renewed certificate is a day old a
// day after it was made.
const DeviceCertRenewAfter = 24 * time.Hour
