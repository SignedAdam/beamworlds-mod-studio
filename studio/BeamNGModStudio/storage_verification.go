package main

import (
 "context"
 "errors"
 "fmt"
 "path/filepath"
 "strings"
)

type archiveHashKey struct{volume,file,modified string;size int64}

// This is a bounded in-memory verification cache, not archive storage. A path
// replacement, write, or size change invalidates it through filesystem identity.
func(service *AppService)verifiedArchiveHash(ctx context.Context,path string,identity ArchiveFileIdentity)(string,bool,error){
 key:=archiveHashKey{identity.VolumeID,identity.FileID,identity.ModifiedNs,identity.SizeBytes}
 if identity.IdentityKnown{
  service.archiveHashMu.Lock();hash,ok:=service.archiveHashes[key];service.archiveHashMu.Unlock()
  if ok{return hash,false,nil}
 }
 hash,err:=hashFileSHA256(ctx,path);if err!=nil{return "",false,err}
 after,err:=inspectArchiveFile(path);if err!=nil{return "",false,err}
 if !sameArchiveObject(identity,after){return "",false,fmt.Errorf("archive changed while hashing: %s",path)}
 service.archiveHashMu.Lock()
 if service.archiveHashes==nil || len(service.archiveHashes)>=4096{service.archiveHashes=make(map[archiveHashKey]string)}
 service.archiveHashes[key]=hash
 service.archiveHashMu.Unlock()
 return hash,true,nil
}

func(service *AppService)verifyStorageCleanupCandidates(ctx context.Context,items []StorageAuditItem,progress *StorageProgress)error{
 for i:=range items{
  if err:=ctx.Err();err!=nil{return err}
  item:=&items[i]
  if !item.CleanupAllowed{continue}
  source,exists,err:=archiveIdentityIfPresent(item.SourcePath)
  valid:=err==nil && exists && source.Regular && !samePath(item.Path,item.SourcePath)
  if valid{
   resolvedSource,sourceErr:=filepath.EvalSymlinks(item.SourcePath)
   resolvedTarget,targetErr:=filepath.EvalSymlinks(item.Path)
   valid=sourceErr==nil && targetErr==nil && !samePath(resolvedSource,resolvedTarget)
  }
  if valid && !sameArchiveFileID(source,item.Identity){
   progress.Phase="verifying";progress.Current=item.Path;service.emitStorageProgress(*progress)
   sourceHash,read,sourceErr:=service.verifiedArchiveHash(ctx,item.SourcePath,source)
   if read{progress.BytesProcessed+=source.SizeBytes}
   targetHash,read,targetErr:=service.verifiedArchiveHash(ctx,item.Path,item.Identity)
   if read{progress.BytesProcessed+=item.Identity.SizeBytes}
   if errors.Is(sourceErr,context.Canceled) || errors.Is(targetErr,context.Canceled){return context.Canceled}
   if errors.Is(sourceErr,context.DeadlineExceeded) || errors.Is(targetErr,context.DeadlineExceeded){return context.DeadlineExceeded}
   valid=sourceErr==nil && targetErr==nil && sourceHash==targetHash && (item.SHA256=="" || strings.EqualFold(item.SHA256,sourceHash))
  }
  if valid{
   item.SourceIdentity=source
   item.Reason+="; canonical content verified"
   continue
  }
  item.CleanupAllowed=false;item.ReclaimableBytes=0
  item.Reason="Canonical content could not be verified; archive preserved for review"
  if item.Classification==classLegacyCacheRedundant{item.Classification=classLegacyCacheOnly;item.RecoveryAllowed=true}
  if item.Classification==classCollectionMirror{item.RecoveryAllowed=true}
 }
 return nil
}
