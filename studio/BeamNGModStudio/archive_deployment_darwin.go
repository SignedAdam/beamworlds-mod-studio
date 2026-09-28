//go:build darwin

package main

import (
 "errors"
 "golang.org/x/sys/unix"
)

func renameArchiveNoReplace(from,to string)error{
 err:=unix.RenamexNp(from,to,unix.RENAME_EXCL)
 if errors.Is(err,unix.ENOTSUP){return linkArchiveNoReplace(from,to)}
 return err
}
func archiveDestinationFileLimit(path string)(int64,error){
 var info unix.Statfs_t
 if err:=unix.Statfs(nearestExistingDir(path),&info);err!=nil{return 0,err}
 if unix.ByteSliceToString(info.Fstypename[:])=="msdos"{return (1<<32)-1,nil}
 return 0,nil
}
