# RPC lessons

- The fact consumer needs the Open-derived context during callback work, not only
  network reads. Explicitly attach the request to the returned HTTP response and
  test deadline expiry through response.Request.Context() without a pending read.

- Clean transport EOF is not consumer completion: JSON decoders may retain
  complete buffered records. Preserve the request context and deadline at EOF
  in both regular and limit-probe reads; let explicit Body.Close cancel it after
  callbacks. Test callback context after real HTTP EOF, not just while reading.

- Redact known credential sources, not arbitrary request values. Preserve public
  IDs, header metadata and authentication scheme names in HTTPError.Body; test
  preservation and credential non-disclosure together at the real HTTP boundary.
