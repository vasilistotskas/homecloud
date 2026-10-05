This folder is a homecloud archive of an iCloud Photos library.
Everything here is ordinary files; no homecloud software is needed to use it.

Photos/YYYY/MM/
    Every photo and video, original bytes as stored in iCloud, in folders by
    the month it was taken. Edited versions, Live Photo videos and RAW
    alternatives sit next to their originals.

Photos/Hidden/YYYY/MM/
    Photos and videos hidden in iCloud Photos (when homecloud is set to archive
    them), kept apart. The folder is hidden in file managers (Windows: hidden
    attribute; Linux: listed in Photos/.hidden); show hidden items to see it.

*.xmp (next to each file)
    XMP metadata sidecars: capture date and time zone, GPS location, rating
    and favourite, title and caption, albums and people. Standard XMP, read by
    Lightroom, darktable, digiKam, Immich and most photo managers. The media
    files themselves are never modified.

manifest.csv
    One row per file, rewritten after every sync:
      library, asset_id, version   iCloud's identity of the file
      filename                      original file name
      path                          location below this folder (forward slashes)
      media_type                    photo, video, live_photo_image, live_photo_video
      size_bytes, sha256            exact size and SHA-256 of the file
      state                         discovered, downloaded, verified or quarantined
      created_at, added_at          when it was taken / added to iCloud (UTC)
      verified_at                   when homecloud last confirmed the checksum (UTC)
    To check a file yourself, compare its SHA-256 with the manifest
    (Windows: certutil -hashfile <file> SHA256; Linux: sha256sum <file>).

homecloud.db
    homecloud's ledger (SQLite): verification history and an append-only
    audit log. Open it with any SQLite tool.

kei/
    State of the downloader (kei). logs/ holds homecloud and kei logs.
