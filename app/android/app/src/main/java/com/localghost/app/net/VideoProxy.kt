// This file is left empty so a tree a drop was copied over holds what the drop holds:
// tools/apply_removed.sh deletes it (server/tools/removed.txt). The loopback video proxy it
// held has had no caller since the player began reading through BoxDataSource; a plain-http
// server on 127.0.0.1 handing out vault video is not something to leave lying in the tree.
package com.localghost.app.net
