//go:build linux

package main

import (
 "errors"
 "golang.org/x/sys/unix"
)

func renameArchiveNoReplace(from,to string)error{
 err:=unix.Renameat2(unix.AT_FDCWD,from,unix.AT_FDCWD,to,unix.RENAME_NOREPLACE)
 if errors.Is(err,unix.ENOSYS) || errors.Is(err,unix.EINVAL) || errors.Is(err,unix.EOPNOTSUPP){return linkArchiveNoReplace(from,to)}
 return err
}
func archiveDestinationFileLimit(path string)(int64,error){
 var info unix.Statfs_t
 if err:=unix.Statfs(nearestExistingDir(path),&info);err!=nil{return 0,err}
 if info.Type==unix.MSDOS_SUPER_MAGIC{return (1<<32)-1,nil}
 return 0,nil
}
