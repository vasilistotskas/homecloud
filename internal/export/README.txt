This folder is a homecloud archive of an iCloud Photos library and of iCloud
Drive.
Everything here is ordinary files; no homecloud software is needed to use it.

Photos/YYYY/MM/
    Every photo and video, original bytes as stored in iCloud, in folders by
    the month it was taken. Edited versions, Live Photo videos and RAW
    alternatives sit next to their originals. A few files may sit in folders
    named after an iCloud album, from an earlier downloader setting;
    manifest.csv lists the exact path of every file.

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
      verified_at                   when homecloud last confirmed the checksum (UTC);
                                    a share of files is re-checked every week
    To check a file yourself, compare its SHA-256 with the manifest
    (Windows: certutil -hashfile <file> SHA256; Linux: sha256sum <file>).

safe-to-delete.csv
    The photos and videos you can delete from iCloud Photos (in Apple's Photos
    app) without losing them, rewritten after every sync, newest first. An
    item is listed only when every one of its files here was verified and then
    confirmed again by a later check, it has been in iCloud Photos longer than
    homecloud's safe_to_delete.min_age, and the drives holding this folder were
    last found healthy (otherwise the file lists nothing). homecloud itself
    never deletes anything from iCloud; Photos keeps what you delete in
    Recently Deleted for 30 days.
      created_at, added_at          when it was taken / added to iCloud (UTC)
      filename, kind                original file name; photo, live or video
      size_bytes                    size of all its files here (original, edits,
                                    Live Photo video): about the space it frees
      path                          its original below this folder
      library, asset_id             iCloud's identity of the item

Drive/
    iCloud Drive, archived by homecloud. Nothing here is ever deleted.
    Drive/iCloud Drive/...        the iCloud Drive folder as the Files app shows it
    Drive/App Libraries/<App> (<id>)/...
                                  each app's own iCloud folder (Books, Pages, ...)
    Drive/.versions/<time>/...    every copy a newer version replaced (UTC time)
    Drive/manifest.csv            one row per archived file:
      icloud_path                 where the file is (or was) in iCloud
      path                        location below Drive/ (names made safe for Windows)
      size_bytes, sha256          exact size and SHA-256 of the archived copy
      apple_signature             Apple's own checksum, checked at download
      package                     true for iWork documents, books and other
                                  bundles: kept as the ZIP archive iCloud serves
                                  for them; any unzip tool extracts the bundle
      modified_at, archived_at    when it changed in iCloud / was archived (UTC)
      verified_at                 when homecloud last confirmed the checksum (UTC)
      deleted_in_icloud_at        set when the file is gone from iCloud; the copy stays

homecloud.db
    homecloud's ledger (SQLite): verification history and an append-only
    audit log. Open it with any SQLite tool.

kei/
    The downloader's (kei) configuration, written by homecloud, and its last
    sync report. kei keeps its own state outside this folder.

logs/
    homecloud and kei logs (Windows; on Linux they go to the systemd journal).

.cache/
    Thumbnails for the homecloud app. Derived data: safe to delete.
