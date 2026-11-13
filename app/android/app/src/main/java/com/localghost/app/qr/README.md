# qr , reading the enrolment code from scratch

A QR decoder written by hand, no library. The box has a from-scratch QR encoder, and this is its
inverse, restricted to exactly the subset the enrolment QR uses (byte mode, versions one to ten, error
correction level M). Writing it rather than pulling a dependency keeps the app's dependency list near
zero and means the whole enrolment path, both ends, is code we own and can verify.

The pipeline has two halves. QrSampler is the image-processing half, taking a grayscale frame and
turning it into a grid of dark and light modules by binarising, finding the three finder patterns, and
sampling each module's centre. QrMatrixDecode is the data half, reading that grid back to bytes.
GaloisField and ReedSolomon are the error correction below them, ported faithfully from a validated
reference and covered by round-trip tests that check it corrects up to capacity and rejects beyond it
rather than mis-correcting.

The decoder is tested end to end on rendered frames (`QrScanEndToEndTest`): a real code drawn into
a camera-sized frame at a size, an angle, a blur and a noise, then put through exactly what the
analyser runs. That test exists because the scanner read large codes only in a narrow band of
distances and nothing showed why. Four causes, all in the sampler, fixed on 13 November 2026: the
row scan tried only every third dark run as a finder's start (so a finder with a dark page beside
it was never found); a dense code's own data out-ranked its real corners; a turned code's module
size was measured on a slant; and a speck of data could pass for the alignment pattern.

What the person is told while it is not reading is in `ScanCoach` (move closer, move back, hold
steady), which says nothing once the code has left the frame.
