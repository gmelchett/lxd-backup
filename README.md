# LXD-backup

LXD-backup is a simple wrapper around `lxc export [containername] [filename] --instance-only --compression zstd`
that produces deltas. LXD-backup creates a full snapshot each quarter and then produces deltas compared to the
quarter backup. It does not include snapshots.

 * Quarter backups - Full exports. Lasts forever
 * Monthly deltas - Overwritten after a year
 * Week number of month - IE, 1,2 and 3 (except February) Lasts a month
 * Week day number - Everyday delta, last a week (0 = Sunday)

LXD-backup is intended to be execute once a day from a cronjob. Example:
```
0       2       *       *       *       /home/me/lxd-backup -b /lxd-backups
```
Starts a backup job every night at two o'clock. Please notice that running containers
will be stopped before a backup can be done. Such a container will be restarted once
the backup is done.

There are no deltas towards deltas. - I'm considering implementing it.

## Output

The quarter backup looks like this:
 * `lxd-backup-name-Q20223.tar.zst` which is a `lxc export` backup.
 * `lxd-backup-name-Q20223.tar.zst.md5sum` which is a text file listing md5sums of all files in the backup.
 * `lxd-backup-name-Q20223.tar.zst.profilename.profile` which is the profile the container uses

where `name` is the container name and `profilename` is the profile that the `name` container uses.

The delta backups looks a little different:

* `lxd-backup-name-WN0-delta.tar.zst` includes new/changed files compared to the quarter backup
* `lxd-backup-name-WN0-delta.tar.zst.removed` includes list of files that has been removed since the quarter
* `lxd-backup-name-WN0-delta.tar.zst.profilename.profile` same as for quarter backup

## Restoring a backup

You have to manually do the job of creating a new tar-ball for `lxc import` by combining the quarter
backup with the wanted delta. IE overwrite/add the changes from the delta and remove the removed files.
I think you can do that with some `tar` commands, or just use `midnight commander`.

## Runtime dependencies
LXD of course and zstd. I think zstd compression algorithm offers a good compression ratio considering
the CPU cycles needed.

## Building
Install go.

`go get -u && CGO_ENABLED=0 go build`

The `CGO_ENABLED=0` isn't always needed, except if you want to run the output on another
dist/version.

## Configuring
```
Usage of ./lxd-backup:
  -b string
        Backup output directory.
  -ec string
        Containers to exclude from backup. Comma separated.
  -eh string
        Hosts to exclude from backup. Comma separated.
  -ic string
        Containers to include in backup. Comma separated.
  -ih string
        Hosts to include in backup. Comma separated.
  -v    Enable verbose printing.
```

By default, all containers are included. If you use any include arguments, only the included
hosts/containers will be backed-up, and if you use any exclude arguments, all hosts/containers
except listed will be backed-up.


## * WARNING * WARNING * WARNING *

Consider this simple piece of software beta software. Manually verify that the backups include
what you think they should include. There are NO warranties.

## Licence

MIT.
