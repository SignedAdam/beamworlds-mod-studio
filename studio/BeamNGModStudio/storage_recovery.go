package main

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "strings"
 "time"

 modkit "github.com/SignedAdam/beamworlds-modkit"
)

// Recovery is an explicit import, not activation. Its source stays intact until
// verified canonical bytes, indexing, and the recovery journal are durable.
func(service *AppService)RecoverStorageArchive(ctx context.Context,fingerprint,itemID string)(result StorageCleanupResult,resultErr error){
 id,err:=modkit.NewID();if err!=nil{return result,err}
 result=StorageCleanupResult{OperationID:id,Failures:[]string{},RetainedPaths:[]string{},ReclaimedEstimate:true}
 progress:=StorageProgress{OperationID:id,Phase:"validating",Total:1}
 service.emitStorageProgress(progress)
 var journal *deploymentJournalEntry
 outcome:=storageCleanupJournalEntry{ItemID:itemID,Action:"failed"}
 preserveJournal:=false
 locked:=false
 defer func(){
  journalID:=""
  if journal!=nil{
   journalID=journal.ID
   if resultErr!=nil && !preserveJournal {
    recoveryCtx,cancel:=context.WithTimeout(context.WithoutCancel(ctx),10*time.Second)
    cleanupErr:=service.discardRecoveryPreparation(recoveryCtx,*journal);cancel()
    if cleanupErr!=nil{resultErr=errors.Join(resultErr,cleanupErr);preserveJournal=true}
   }
   if preserveJournal{journalID=""}
  }
  service.finalizeStorageOperation(ctx,journalID,[]storageCleanupJournalEntry{outcome},&result,&progress,&resultErr)
  if locked{service.modImportMu.Unlock()}
 }()
 if fingerprint=="" || itemID==""{return result,errors.New("review the archive before recovering it")}
 service.modImportMu.Lock();locked=true
 if err:=ctx.Err();err!=nil{return result,err}
 if err:=service.requireGameStopped();err!=nil{return result,err}
 audit,err:=service.AuditArchiveStorage(ctx);if err!=nil{return result,err}
 if audit.Fingerprint!=fingerprint{return result,errors.New("storage changed since review; audit again")}
 var target *StorageAuditItem
 for i:=range audit.Items{if audit.Items[i].ID==itemID{target=&audit.Items[i];break}}
 if target==nil || !target.RecoveryAllowed{return result,errors.New("this archive is not eligible for reviewed recovery")}
 if err:=service.validateStorageRecoverySource(target.Path);err!=nil{return result,err}
 original,exists,err:=archiveIdentityIfPresent(target.Path);if err!=nil{return result,err}
 if !exists || !sameArchiveObject(original,target.Identity){return result,errors.New("recovery source changed since review")}
 outcome.Path,outcome.Classification=target.Path,target.Classification
 progress.Phase="verifying";progress.Current=filepath.Base(target.Path);service.emitStorageProgress(progress)
 manifest,err:=modkit.Inspect(ctx,target.Path);if err!=nil{return result,err}
 if !manifest.ValidArchive{return result,errors.New("recovery source is not a valid archive")}
 checksum,err:=modkit.FullSHA256(ctx,target.Path);if err!=nil{return result,err}
 after,err:=inspectArchiveFile(target.Path);if err!=nil{return result,err}
 if !sameArchiveObject(original,after){return result,errors.New("recovery source changed during verification")}
 destination,err:=service.modImportDestination();if err!=nil{return result,err}
 if pathWithin(destination,service.config.ActiveModsDir) || pathWithin(destination,service.config.ProfileDir) || pathWithin(destination,service.config.ExportDir){return result,errors.New("canonical library must be outside active mods and generated archive storage")}
 canonical,canonicalRoot,err:=service.existingRecoveryCanonical(ctx,checksum,manifest.CentralFingerprint,original)
 if err!=nil{return result,err}
 reuse:=canonical!=""
 if reuse{destination=canonicalRoot}else{canonical,reuse,err=recoveryCanonicalPath(ctx,destination,target.Path,manifest.Title,checksum,original.SizeBytes);if err!=nil{return result,err}}
 plan:=ArchiveDeploymentPlan{Fingerprint:fingerprint,DestinationRoot:destination,Entries:[]ArchiveDeploymentEntry{{EntityID:target.EntityID,ArtifactID:target.ArtifactID,SourcePath:target.Path,DestinationPath:canonical,SHA256:checksum,SizeBytes:original.SizeBytes,SourceIdentity:original}}}
 j:=deploymentJournalEntry{ID:id,OperationID:id,State:journalStatePlanned,Purpose:storageRecoveryPurpose,OwnerID:id,DestinationRoot:destination,StagingDir:filepath.Join(destination,".beamworlds-recovery-"+id),Plan:plan,StartedAt:nowUTC()}
 if err:=service.store.writeDeploymentJournal(ctx,j);err!=nil{return result,err};journal=&j
 if !reuse {
  if err:=os.Mkdir(j.StagingDir,0755);err!=nil{return result,err}
  stage:=filepath.Join(j.StagingDir,"payload.staging")
  prepared:=OwnedArchiveEntry{ID:"payload",Purpose:storageRecoveryPurpose,OwnerID:id,SourcePath:target.Path,SourceIdentity:original,TargetRoot:j.StagingDir,RelativePath:"payload.staging",SHA256:checksum,TargetIdentity:original}
  j.PreparedEntries=[]OwnedArchiveEntry{prepared}
  if err:=service.store.savePreparedArchiveEntry(ctx,id,prepared);err!=nil{return result,err}
  progress.Phase="recovering";service.emitStorageProgress(progress)
  if linkErr:=os.Link(target.Path,stage);linkErr!=nil {
   limit,err:=archiveDestinationFileLimit(destination);if err!=nil{return result,err}
   if limit>0 && original.SizeBytes>limit{return result,fmt.Errorf("archive exceeds the destination filesystem's %d-byte file limit",limit)}
   free,err:=availableArchiveBytes(destination);if err!=nil{return result,err}
   if free<original.SizeBytes{return result,fmt.Errorf("recovery needs %d bytes of copy space; %d available",original.SizeBytes,free)}
   output,err:=os.OpenFile(stage,os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600);if err!=nil{return result,err}
   identity,err:=inspectArchiveFile(stage);if err!=nil{output.Close();return result,err}
   prepared.TargetIdentity=identity;j.PreparedEntries[0]=prepared
   if err:=service.store.savePreparedArchiveEntry(ctx,id,prepared);err!=nil{output.Close();return result,err}
   copied,_,copyErr:=copyArchivePayload(ctx,target.Path,output,checksum,func(bytes int64){progress.BytesProcessed=bytes;service.emitStorageProgress(progress)})
   closeErr:=output.Close();progress.BytesProcessed=copied
   if err:=errors.Join(copyErr,closeErr);err!=nil{return result,err}
  }
  identity,err:=inspectArchiveFile(stage);if err!=nil{return result,err}
  prepared.TargetIdentity=identity;j.PreparedEntries[0]=prepared
  if err:=service.store.savePreparedArchiveEntry(ctx,id,prepared);err!=nil{return result,err}
  j.MovedFiles=[]movedFileRecord{{From:stage,To:canonical,SourceIdentity:identity}}
  j.State=journalStateActivating
  if err:=service.store.writeDeploymentJournal(ctx,j);err!=nil{return result,err}
  if err:=service.requireGameStopped();err!=nil{return result,err}
  if err:=executeArchiveMove(j.MovedFiles[0]);err!=nil{return result,err}
  preserveJournal=true // published bytes must remain recoverable if indexing fails
 }
 info,err:=os.Stat(canonical);if err!=nil{return result,err}
 canonicalIdentity,err:=inspectArchiveFile(canonical);if err!=nil{return result,err}
 if !sameArchiveFileID(original,canonicalIdentity){
  verified,err:=modkit.FullSHA256(ctx,canonical);if err!=nil{return result,err}
  if verified!=checksum{return result,errors.New("canonical archive checksum verification failed")}
 }
 sourceAfter,err:=inspectArchiveFile(target.Path);if err!=nil{return result,err}
 canonicalAfter,err:=inspectArchiveFile(canonical);if err!=nil{return result,err}
 if !sameArchiveObject(original,sourceAfter) || !sameArchiveObject(canonicalIdentity,canonicalAfter){return result,errors.New("recovery files changed during verification")}
 manifest=modImportManifestForPath(manifest,canonical,info,checksum)
 imported,err:=service.indexImportedArchive(ctx,destination,canonical,manifest);if err!=nil{return result,err}
 result.Recovered=1
 j.Plan.Entries[0].EntityID,j.Plan.Entries[0].ArtifactID=imported.EntityID,imported.ArtifactID
 j.Plan.Entries[0].TargetIdentity=canonicalIdentity
 j.State=journalStateApplied
 if err:=service.store.writeDeploymentJournal(ctx,j);err!=nil{return result,err}
 if err:=service.requireGameStopped();err!=nil{return result,err}
 verifiedCanonical,err:=inspectArchiveFile(canonical);if err!=nil{return result,err}
 if !sameArchiveObject(canonicalIdentity,verifiedCanonical){return result,errors.New("canonical archive changed before retiring the original")}
 if err:=service.adoptRecoveredOriginal(ctx,target.Path,original,imported,canonicalIdentity);err!=nil{return result,err}
 if pathWithin(target.Path,filepath.Join(service.config.ActiveModsDir,managedModDirectoryName)) || pathWithin(target.Path,filepath.Join(service.config.ExportDir,collectionFolderDirectory)) {
  result.RetainedPaths=append(result.RetainedPaths,target.Path)
 } else {
  current,exists,err:=archiveIdentityIfPresent(target.Path);if err!=nil{return result,err}
  if !exists || !sameArchiveObject(current,original){return result,errors.New("original archive changed before retirement")}
  if err:=os.Remove(target.Path);err!=nil{result.RetainedPaths=append(result.RetainedPaths,target.Path);return result,err}
 }
 if err:=removeEmptyArchiveDirectory(j.StagingDir);err!=nil{return result,err}
 preserveJournal=false
 outcome.Action="recovered";outcome.Detail=canonical
 progress.Completed=1
 if service.emit!=nil{service.emit("library:item",imported)}
 return result,nil
}

func recoveryCanonicalPath(ctx context.Context,root,source,title,checksum string,size int64)(string,bool,error){
 base:=filepath.Base(source)
 if strings.HasSuffix(strings.ToLower(base),".zip.stop"){base=strings.TrimSuffix(base,".stop")}else if !strings.EqualFold(filepath.Ext(base),".zip"){base=strings.TrimSuffix(base,filepath.Ext(base))+".zip"}
 // Legacy cache files are named by checksum; prefer the mod's own title.
 if stem:=strings.TrimSuffix(base,filepath.Ext(base));len(stem)==64 && strings.EqualFold(stem,checksum){
  if label:=sanitizeArchiveLabel(title);label!=""{base=label+".zip"}
 }
 stem:=sanitizeArchiveLabel(strings.TrimSuffix(base,filepath.Ext(base)))
 if runes:=[]rune(stem);len(runes)>80{stem=string(runes[:80])}
 for attempt:=0;attempt<8;attempt++{
  name:=base
  if attempt==1{name=stem+"-"+checksum+".zip"}else if attempt>1{id,err:=modkit.NewID();if err!=nil{return "",false,err};name=stem+"-"+id+".zip"}
  candidate:=filepath.Join(root,name)
  identity,exists,err:=archiveIdentityIfPresent(candidate)
  if err!=nil{if _,statErr:=os.Lstat(candidate);statErr!=nil{return "",false,err};continue}
  if !exists{return candidate,false,nil}
  if identity.SizeBytes!=size{continue}
  hash,err:=modkit.FullSHA256(ctx,candidate);if err!=nil{return "",false,err}
  after,err:=inspectArchiveFile(candidate);if err!=nil{return "",false,err}
  if hash==checksum && sameArchiveObject(identity,after){return candidate,true,nil}
 }
 return "",false,errors.New("could not reserve a collision-free canonical archive name")
}

func(service *AppService)validateStorageRecoverySource(path string)error{
 roots:=[]string{filepath.Join(service.config.ProfileDir,legacyArchiveCacheDirectory),filepath.Join(service.config.ActiveModsDir,managedModDirectoryName),filepath.Join(service.config.ExportDir,collectionFolderDirectory),service.archiveTransactionWorkRoot(archivePurposePlay),service.archiveTransactionWorkRoot(archivePurposeCollection)}
 for _,root:=range roots{if pathWithin(path,root){return validateArchiveChild(path,root)}}
 journals,err:=service.store.listPendingDeploymentJournals(context.Background());if err!=nil{return err}
 for _,j:=range journals{
  if j.Purpose!=storageRecoveryPurpose{continue}
  for _,entry:=range j.PreparedEntries{if samePath(path,filepath.Join(entry.TargetRoot,entry.RelativePath)){return validateArchiveChild(path,j.StagingDir)}}
 }
 return errors.New("recovery source is outside reviewed generated archive storage")
}

func(service *AppService)adoptRecoveredOriginal(ctx context.Context,path string,original ArchiveFileIdentity,item LibraryItem,canonical ArchiveFileIdentity)error{
 current,exists,err:=archiveIdentityIfPresent(path);if err!=nil{return err}
 if !exists || !sameArchiveObject(current,original){return errors.New("original archive changed before ownership adoption")}
 managed:=pathWithin(path,filepath.Join(service.config.ActiveModsDir,managedModDirectoryName))
 mirror:=pathWithin(path,filepath.Join(service.config.ExportDir,collectionFolderDirectory))
 if !managed && !mirror{return nil}
 entries,err:=service.store.listOwnedArchiveEntries(ctx);if err!=nil{return err}
 var owned OwnedArchiveEntry
 for _,entry:=range entries{if samePath(filepath.Join(entry.TargetRoot,entry.RelativePath),path){owned=entry;break}}
 if owned.ID==""{
  if !managed{return errors.New("an unowned collection file remains protected; its canonical archive was recovered")}
  id,err:=modkit.NewID();if err!=nil{return err}
  owned=OwnedArchiveEntry{ID:id,Purpose:archivePurposePlay,OwnerID:playDeploymentOwnerID,TargetRoot:filepath.Dir(path),RelativePath:filepath.Base(path)}
 }
 owned.EntityID,owned.ArtifactID,owned.SHA256=item.EntityID,item.ArtifactID,item.SHA256
 owned.SourcePath,owned.SourceIdentity,owned.TargetIdentity=item.ArchivePath,canonical,original
 owned.Method=deployMethodCopy;if sameArchiveFileID(original,canonical){owned.Method=deployMethodHardlink}
 owned.State=archiveStateActive
 return service.store.saveOwnedArchiveEntries(ctx,[]OwnedArchiveEntry{owned})
}

func(service *AppService)discardRecoveryPreparation(ctx context.Context,j deploymentJournalEntry)error{
 if j.StagingDir==""{return nil}
 if !samePath(j.StagingDir,filepath.Join(j.DestinationRoot,".beamworlds-recovery-"+j.OperationID)){return errors.New("invalid recovery staging directory")}
 if len(j.Plan.Entries)!=1{return errors.New("invalid recovery source snapshot")}
 source:=j.Plan.Entries[0]
 current,sourceExists,sourceErr:=archiveIdentityIfPresent(source.SourcePath)
 for _,entry:=range j.PreparedEntries{
  path:=filepath.Join(entry.TargetRoot,entry.RelativePath)
  if err:=validateArchiveChild(path,j.StagingDir);err!=nil{return err}
  identity,exists,err:=archiveIdentityIfPresent(path);if err!=nil{return err};if !exists{continue}
  if sourceErr!=nil || !sourceExists || !sameArchiveObject(current,source.SourceIdentity){return errors.New("recovery source changed; staging data retained")}
  if !sameArchiveFileID(identity,entry.TargetIdentity){return errors.New("recovery staging file changed; preserved")}
  if err:=os.Remove(path);err!=nil{return err}
 }
 return removeEmptyArchiveDirectory(j.StagingDir)
}

// Caller holds modImportMu. Unfinished copies are removed only by recorded file
// identity. Published canonical files are verified and indexed, never discarded.
func(service *AppService)reconcileStorageJournals(ctx context.Context)error{
 journals,err:=service.store.listPendingDeploymentJournals(ctx);if err!=nil{return err}
 for _,j:=range journals{
  if j.Purpose==storageCleanupPurpose{
   var items []storageCleanupJournalEntry
   if j.ErrorMessage!=""{if err:=json.Unmarshal([]byte(j.ErrorMessage),&items);err!=nil{return err}}
   for i:=range items{if items[i].Action=="pending"{_,err:=os.Lstat(items[i].Path);if errors.Is(err,os.ErrNotExist){items[i].Action="absent";items[i].Detail="Target absent after interruption; no reclaimed-byte claim"}else if err!=nil{return err}else{items[i].Action="retained";items[i].Detail="Interrupted before confirmed removal; review again"}}}
   payload,err:=json.Marshal(items);if err!=nil{return err}
   if err:=service.store.completeDeploymentJournal(ctx,j.ID,journalStateFailed,string(payload));err!=nil{return err}
   continue
  }
  if j.Purpose!=storageRecoveryPurpose{continue}
  if err:=service.requireGameStopped();err!=nil{return err}
  if len(j.Plan.Entries)!=1{return fmt.Errorf("recovery %s lacks a verified source plan",j.ID)}
  entry:=j.Plan.Entries[0]
  if err:=validateArchiveChild(entry.DestinationPath,j.DestinationRoot);err!=nil{return err}
  published,exists,err:=archiveIdentityIfPresent(entry.DestinationPath);if err!=nil{return err}
  if !exists{
   for _,prepared:=range j.PreparedEntries{
    stage:=filepath.Join(prepared.TargetRoot,prepared.RelativePath)
    if err:=validateArchiveChild(stage,j.StagingDir);err!=nil{return err}
    ready,present,err:=archiveIdentityIfPresent(stage);if err!=nil{return err}
    if !present || ready.SizeBytes!=entry.SizeBytes || !sameArchiveObject(ready,prepared.TargetIdentity){continue}
    move:=movedFileRecord{From:stage,To:entry.DestinationPath,SourceIdentity:ready}
    j.MovedFiles=[]movedFileRecord{move};j.State=journalStateActivating
    if err:=service.store.writeDeploymentJournal(ctx,j);err!=nil{return err}
    if err:=executeArchiveMove(move);err!=nil{return err}
    published,exists=ready,true
    break
   }
   if !exists{
    if err:=service.discardRecoveryPreparation(ctx,j);err!=nil{return err}
    if err:=service.store.completeDeploymentJournal(ctx,j.ID,journalStateFailed,"interrupted recovery rolled back; original archive preserved");err!=nil{return err}
    continue
   }
  }
  expected:=entry.TargetIdentity
  if len(j.MovedFiles)>0{expected=j.MovedFiles[0].SourceIdentity}
  if !expected.IdentityKnown {
   if sameArchiveObject(published,entry.SourceIdentity){expected=entry.SourceIdentity}else if j.State==journalStatePlanned && len(j.PreparedEntries)==0{
    if err:=service.store.completeDeploymentJournal(ctx,j.ID,journalStateFailed,"interrupted before canonical indexing; existing files preserved");err!=nil{return err}
    continue
   }else{return errors.New("recovery snapshot is incomplete; files preserved for review")}
  }
  if !sameArchiveObject(published,expected){return errors.New("published recovery archive changed; review required")}
  checksum:=entry.SHA256
  manifest,err:=modkit.Inspect(ctx,entry.DestinationPath);if err!=nil{return err}
  info,err:=os.Stat(entry.DestinationPath);if err!=nil{return err}
  after,err:=inspectArchiveFile(entry.DestinationPath);if err!=nil{return err}
  if !sameArchiveObject(published,after){return errors.New("published recovery archive changed during reindexing")}
  manifest=modImportManifestForPath(manifest,entry.DestinationPath,info,checksum)
  item,err:=service.indexImportedArchive(ctx,j.DestinationRoot,entry.DestinationPath,manifest);if err!=nil{return err}
  original,exists,err:=archiveIdentityIfPresent(entry.SourcePath);if err!=nil{return err}
  if exists && sameArchiveObject(original,entry.SourceIdentity){if err:=service.adoptRecoveredOriginal(ctx,entry.SourcePath,original,item,published);err!=nil{return err}}
  if err:=service.discardRecoveryPreparationAfterPublish(j);err!=nil{return err}
  if err:=service.store.completeDeploymentJournal(ctx,j.ID,journalStateDone,"canonical recovery verified and indexed; original retained for reviewed cleanup");err!=nil{return err}
 }
 return nil
}
func(service *AppService)discardRecoveryPreparationAfterPublish(j deploymentJournalEntry)error{
 if j.StagingDir==""{return nil}
 return removeEmptyArchiveDirectory(j.StagingDir)
}

func(service *AppService)existingRecoveryCanonical(ctx context.Context,checksum,fingerprint string,original ArchiveFileIdentity)(string,string,error){
 rows,err:=service.store.db.QueryContext(ctx,`SELECT l.path,l.root_path FROM archive_links l JOIN artifacts a ON a.id=l.artifact_id WHERE a.sha256=? COLLATE NOCASE OR a.central_fingerprint=? COLLATE NOCASE ORDER BY l.active DESC,l.last_seen_at DESC,l.id`,checksum,fingerprint)
 if err!=nil{return "","",err};defer rows.Close()
 for rows.Next(){
  var path,root string;if err:=rows.Scan(&path,&root);err!=nil{return "","",err}
  if path=="" || pathWithin(path,service.config.ProfileDir) || pathWithin(path,service.config.ExportDir) || pathWithin(path,filepath.Join(service.config.ActiveModsDir,managedModDirectoryName)){continue}
  identity,exists,err:=archiveIdentityIfPresent(path);if err!=nil || !exists || identity.SizeBytes!=original.SizeBytes{continue}
  same:=sameArchiveFileID(identity,original)
  if !same{hash,err:=modkit.FullSHA256(ctx,path);if err!=nil{return "","",err};same=hash==checksum}
  after,err:=inspectArchiveFile(path);if err!=nil{return "","",err}
  if same && sameArchiveObject(identity,after){if root==""{root=filepath.Dir(path)};return path,root,nil}
 }
 return "","",rows.Err()
}
