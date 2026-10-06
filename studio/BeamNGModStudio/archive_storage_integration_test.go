package main

import (
	"context"
	"bytes"
	"os"
	"path/filepath"
	"sync/atomic"
	"strings"
	"testing"
	"time"
)

func reviewedStoragePlay(t *testing.T,service *AppService,collectionID string) PlayRequest {
	t.Helper()
	selection,err:=service.ResolvePlaySelection([]string{collectionID},nil)
	if err!=nil {t.Fatal(err)}
	request:=PlayRequest{CollectionIDs:[]string{collectionID},Fingerprint:selection.Fingerprint,AllowCopy:true}
	plan,err:=service.PlanPlayDeployment(context.Background(),request)
	if err!=nil {t.Fatal(err)}
	request.DeploymentFingerprint=plan.Fingerprint
	return request
}

func TestArchiveRemovalRetiresOwnedGameAndCollectionAliases(t *testing.T) {
	service:=newTestAppService(t)
	items,collectionID:=scanAndCreateCollection(t,service,"Owned aliases",1,9600)
	activation,err:=service.activatePlaySelectionDirect(context.Background(),reviewedStoragePlay(t,service,collectionID))
	if err!=nil {t.Fatal(err)}
	mirror:=syncReviewedCollectionFolder(t,service,collectionID)
	mirrorPath:=ownedMirrorPath(t,service,collectionID,items[0].EntityID)
	if err:=os.WriteFile(filepath.Join(mirror.Path,"personal.zip"),[]byte("user archive"),0600);err!=nil {t.Fatal(err)}
	result,err:=service.DeleteModArchives([]string{items[0].EntityID})
	if err!=nil || result.Forgotten!=1 || len(result.Failures)!=0 {t.Fatalf("removal = %#v, %v",result,err)}
	if _,err:=os.Stat(mirrorPath);!os.IsNotExist(err) {t.Fatal("collection alias retained retired mod",err)}
	files,err:=os.ReadDir(activation.ModsPath)
	if err!=nil {t.Fatal(err)}
	for _,file:=range files {if isModArchive(file.Name()){t.Fatal("game alias retained retired mod",file.Name())}}
	data,err:=os.ReadFile(filepath.Join(mirror.Path,"personal.zip"));if err!=nil || string(data)!="user archive" {t.Fatal("unowned mirror file was changed",err)}
	if _,err:=os.Stat(items[0].ArchivePath);!os.IsNotExist(err){t.Fatal("canonical archive not retired",err)}
	state,err:=service.GetPlayRuntimeState();if err!=nil {t.Fatal(err)}
	if state.Applied {t.Fatal("runtime still reported obsolete selection as applied")}
}

func TestArchiveMutationRefusesRunningGame(t *testing.T) {
	service:=newTestAppService(t)
	items,_:=scanAndCreateCollection(t,service,"Game running",1,9620)
	service.gameRunning=func()(bool,error){return true,nil}
	if _,err:=service.DeleteModArchives([]string{items[0].EntityID});err==nil {t.Fatal("removed archive while game was running")}
	if _,err:=os.Stat(items[0].ArchivePath);err!=nil {t.Fatal("running-game refusal changed archive",err)}
}

func TestArchiveScannerExcludesExplicitGeneratedRoots(t *testing.T) {
	service:=newTestAppService(t)
	service.config.LibraryDir=filepath.Join(service.config.DataDir,"library")
	canonical:=filepath.Join(service.config.LibraryDir,"nested","canonical.zip")
	writeLibraryScanArchive(t,canonical,"Canonical")
	generated:=filepath.Join(service.config.ActiveModsDir,managedModDirectoryName)
	writeLibraryScanArchive(t,filepath.Join(generated,"alias.zip"),"Generated")
	work:=filepath.Join(filepath.Dir(service.config.ActiveModsDir),".beamworlds-deployment","stage")
	writeLibraryScanArchive(t,filepath.Join(work,"stale.zip"),"Stale")
	engine:=NewLibraryEngine(service.store,service.config,func(string,any){})
	for _,root:=range []string{generated,work,service.config.LibraryDir} {
		jobs:=make(chan archiveJob,10)
		var discovered atomic.Int64
		if err:=engine.discoverRoot(context.Background(),root,jobs,&discovered,func(string,string,bool,error){});err!=nil {t.Fatal(err)}
		close(jobs)
		if root==service.config.LibraryDir {
			if discovered.Load()!=1 {t.Fatal("canonical library nested in Studio data was not scanned")}
		} else if discovered.Load()!=0 {t.Fatal("generated archives were rediscovered as canonical mods",root)}
	}
}

func TestArchiveFileRemovalPreservesLastAvailableCopy(t *testing.T) {
	service:=newTestAppService(t)
	root:=filepath.Join(service.config.DataDir,"library")
	first:=modFamilyScanArchive(t,root,filepath.Join("one","same.zip"),"Same","Author","1.0","","same-sha","same-fingerprint",100,testArchiveModified(1),0)
	second:=modFamilyScanArchive(t,root,filepath.Join("two","same.zip"),"Same","Author","1.0","","same-sha","same-fingerprint",100,testArchiveModified(2),0)
	items:=applyLibraryArchives(t,service.store,root,[]ScanArchive{first,second})
	links:=familyArchiveLinks(t,service,items[0].EntityID)
	var target string
	for _,link:=range links {if samePath(link.Path,first.ArchivePath){target=link.ID}}
	if err:=os.Remove(second.ArchivePath);err!=nil {t.Fatal(err)}
	impact,err:=service.PlanArchiveFileRemoval([]string{target});if err!=nil {t.Fatal(err)}
	if len(impact.Refusals)==0 {t.Fatal("missing indexed alternative made the only available file removable")}
	result,err:=service.DeleteArchiveFiles([]string{target})
	if err!=nil {t.Fatal(err)}
	if result.Recycled!=0 || len(result.Failures)==0 {t.Fatalf("last available copy was removed: %#v",result)}
	if _,err:=os.Stat(first.ArchivePath);err!=nil {t.Fatal("last available archive was lost",err)}
}

func TestDeploymentOwnershipCommitFailureRestoresPreviousSelection(t *testing.T) {
	service:=newTestAppService(t)
	service.config.ArchiveDeploymentMode=DeploymentModeCopy
	items,collectionID:=scanAndCreateCollection(t,service,"Rollback",2,9700)
	ctx:=context.Background()
	if _,err:=service.activatePlaySelectionDirect(ctx,reviewedStoragePlay(t,service,collectionID));err!=nil{t.Fatal(err)}
	owned,err:=service.store.listOwnedArchiveEntries(ctx);if err!=nil{t.Fatal(err)}
	marker,err:=os.ReadFile(playRuntimeMarkerPath(service.config));if err!=nil{t.Fatal(err)}
	dbPath:=filepath.Join(service.config.ActiveModsDir,"db.json")
	native,nativeExisted,err:=readArchiveNativeBackup(dbPath);if err!=nil{t.Fatal(err)}
	if _,err:=service.SetCollectionMods(collectionID,[]string{items[0].EntityID},false);err!=nil{t.Fatal(err)}
	request:=reviewedStoragePlay(t,service,collectionID)
	if _,err:=service.store.db.Exec(`CREATE TRIGGER reject_deployment_commit BEFORE UPDATE OF state ON archive_deployment_journal WHEN NEW.state='applied' BEGIN SELECT RAISE(ABORT,'injected ownership commit failure'); END`);err!=nil{t.Fatal(err)}
	if _,err:=service.activatePlaySelectionDirect(ctx,request);err==nil{t.Fatal("faulted ownership commit reported successful activation")}
	for _,entry:=range owned{
		current,err:=inspectArchiveFile(filepath.Join(entry.TargetRoot,entry.RelativePath))
		if err!=nil || !sameArchiveObject(current,entry.TargetIdentity){t.Fatalf("previous deployment file was not restored: %s, %v",entry.RelativePath,err)}
	}
	currentMarker,err:=os.ReadFile(playRuntimeMarkerPath(service.config));if err!=nil || !bytes.Equal(marker,currentMarker){t.Fatal("previous applied marker was not restored",err)}
	currentNative,exists,err:=readArchiveNativeBackup(dbPath);if err!=nil || exists!=nativeExisted || !bytes.Equal(native,currentNative){t.Fatal("native enablement was not restored",err)}
	pending,err:=service.store.listPendingDeploymentJournals(ctx);if err!=nil || len(pending)!=0{t.Fatalf("successful rollback left an unresolved deployment: %#v %v",pending,err)}
}

func TestDeploymentDoesNotOverwriteFileCreatedDuringPreparation(t *testing.T){
	service:=newTestAppService(t)
	_,collectionID:=scanAndCreateCollection(t,service,"Concurrent file",1,9800)
	ctx:=context.Background()
	plan,err:=service.PlanPlayDeployment(ctx,reviewedStoragePlay(t,service,collectionID));if err!=nil{t.Fatal(err)}
	target:=plan.Entries[0].DestinationPath
	service.modImportMu.Lock()
	_,err=service.applyArchiveDeployment(ctx,plan,func()error{return os.WriteFile(target,[]byte("user file created after review"),0600)},nil)
	service.modImportMu.Unlock()
	if err==nil{t.Fatal("an unowned late collision was replaced")}
	content,err:=os.ReadFile(target);if err!=nil || string(content)!="user file created after review"{t.Fatal("late user file was lost",err)}
}

func TestMissingCanonicalArchiveRemainsRecoverableAfterSelectionChange(t *testing.T){
	service:=newTestAppService(t)
	service.config.ArchiveDeploymentMode=DeploymentModeCopy
	items,collectionID:=scanAndCreateCollection(t,service,"Retained old copy",1,9900)
	ctx:=context.Background()
	if _,err:=service.activatePlaySelectionDirect(ctx,reviewedStoragePlay(t,service,collectionID));err!=nil{t.Fatal(err)}
	owned,err:=service.store.listOwnedArchiveEntries(ctx);if err!=nil{t.Fatal(err)}
	if err:=os.Remove(items[0].ArchivePath);err!=nil{t.Fatal(err)}
	if _,err:=service.SetCollectionMods(collectionID,[]string{items[0].EntityID},false);err!=nil{t.Fatal(err)}
	if _,err:=service.activatePlaySelectionDirect(ctx,reviewedStoragePlay(t,service,collectionID));err!=nil{t.Fatal(err)}
	journals,err:=service.store.listPendingDeploymentJournals(ctx);if err!=nil{t.Fatal(err)}
	retained:=false
	for _,journal:=range journals{
		if journal.State!=journalStateCleanup{continue}
		path:=filepath.Join(journal.PreviousDir,owned[0].ID+stagingExtension)
		current,err:=inspectArchiveFile(path)
		if err==nil && sameArchiveObject(current,owned[0].TargetIdentity){retained=true}
	}
	if !retained{t.Fatal("the sole surviving independent archive was discarded")}
	if _,err:=service.activatePlaySelectionDirect(ctx,reviewedStoragePlay(t,service,collectionID));err!=nil{t.Fatal("retained recovery data blocked an unaffected selection",err)}
}

func TestRestartRecoversInterruptedArchiveMoves(t *testing.T){
	for _,installed:=range []bool{false,true}{
		name:="parked";if installed{name="installed"}
		t.Run(name,func(t *testing.T){
			service:=newTestAppService(t)
			service.config.ArchiveDeploymentMode=DeploymentModeCopy
			items,collectionID:=scanAndCreateCollection(t,service,"Restart",1,9950)
			ctx:=context.Background()
			if _,err:=service.activatePlaySelectionDirect(ctx,reviewedStoragePlay(t,service,collectionID));err!=nil{t.Fatal(err)}
			owned,err:=service.store.listOwnedArchiveEntries(ctx);if err!=nil{t.Fatal(err)}
			old:=owned[0]
			plan,err:=service.PlanPlayDeployment(ctx,reviewedStoragePlay(t,service,collectionID));if err!=nil{t.Fatal(err)}
			j:=deploymentJournalEntry{ID:"restart-"+name,OperationID:"restart-"+name,Purpose:archivePurposePlay,OwnerID:playDeploymentOwnerID,State:journalStateActivating,Plan:plan,DestinationRoot:plan.DestinationRoot,PriorOwned:owned,StartedAt:nowUTC()}
			work:=service.archiveTransactionWorkRoot(archivePurposePlay)
			j.StagingDir=filepath.Join(work,"staging-"+j.OperationID)
			j.PreviousDir=filepath.Join(work,"previous-"+j.OperationID)
			for _,dir:=range []string{j.StagingDir,j.PreviousDir}{if err:=os.MkdirAll(dir,0755);err!=nil{t.Fatal(err)}}
			j.NativeBackup,j.NativeExisted,err=readArchiveNativeBackup(filepath.Join(service.config.ActiveModsDir,"db.json"));if err!=nil{t.Fatal(err)}
			j.MarkerBackup,j.MarkerExisted,err=readArchiveNativeBackup(playRuntimeMarkerPath(service.config));if err!=nil{t.Fatal(err)}
			stage:=filepath.Join(j.StagingDir,"prepared"+stagingExtension)
			content,err:=os.ReadFile(items[0].ArchivePath);if err!=nil{t.Fatal(err)}
			if err:=os.WriteFile(stage,content,0600);err!=nil{t.Fatal(err)}
			identity,err:=inspectArchiveFile(stage);if err!=nil{t.Fatal(err)}
			prepared:=makeOwnedEntry("prepared",plan,plan.Entries[0],plan.DestinationRoot,identity)
			j.PreparedEntries=[]OwnedArchiveEntry{prepared}
			target:=filepath.Join(old.TargetRoot,old.RelativePath)
			parked:=filepath.Join(j.PreviousDir,old.ID+stagingExtension)
			j.MovedFiles=[]movedFileRecord{{From:target,To:parked,SourceIdentity:old.TargetIdentity},{From:stage,To:target,SourceIdentity:identity}}
			if err:=service.store.writeDeploymentJournal(ctx,j);err!=nil{t.Fatal(err)}
			if err:=executeArchiveMove(j.MovedFiles[0]);err!=nil{t.Fatal(err)}
			if installed{
				if err:=executeArchiveMove(j.MovedFiles[1]);err!=nil{t.Fatal(err)}
				if err:=os.WriteFile(playRuntimeMarkerPath(service.config),[]byte("interrupted native commit"),0600);err!=nil{t.Fatal(err)}
			}
			databasePath:=service.store.dbPath
			if err:=service.store.Close();err!=nil{t.Fatal(err)}
			reopened,err:=OpenStore(databasePath);if err!=nil{t.Fatal(err)}
			t.Cleanup(func(){_ = reopened.Close()})
			service.store=reopened
			if err:=service.recoverArchiveDeployment(ctx);err!=nil{t.Fatal(err)}
			restored,err:=inspectArchiveFile(target);if err!=nil || !sameArchiveObject(restored,old.TargetIdentity){t.Fatal("restart did not restore the original independent game copy",err)}
			state,err:=service.GetPlayRuntimeState();if err!=nil || !state.Applied || state.Activation.ModCount!=1{t.Fatalf("recovered selection is unusable: %+v %v",state,err)}
			source,err:=os.ReadFile(items[0].ArchivePath);if err!=nil || !bytes.Equal(source,content){t.Fatal("restart changed canonical bytes",err)}
		})
	}
}

func TestLegacyManagedFilesAdoptedOnlyWithVerifiedEvidence(t *testing.T) {
	service := newTestAppService(t)
	items, _ := scanAndCreateCollection(t, service, "Legacy", 3, 9960)
	ctx := context.Background()
	managed := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	if err := os.MkdirAll(managed, 0o755); err != nil { t.Fatal(err) }
	legacyName := func(item LibraryItem) string {
		return sanitizeArchiveLabel(strings.TrimSuffix(filepath.Base(item.ArchivePath), filepath.Ext(item.ArchivePath))) + "-" + shortPlayID(item.EntityID) + ".zip"
	}
	linked, copied, altered := legacyName(items[0]), legacyName(items[1]), legacyName(items[2])
	if err := os.Link(items[0].ArchivePath, filepath.Join(managed, linked)); err != nil { t.Fatal(err) }
	payload, err := os.ReadFile(items[1].ArchivePath)
	if err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(managed, copied), payload, 0o644); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(managed, altered), []byte("same legacy name, different bytes"), 0o644); err != nil { t.Fatal(err) }
	if err := service.writePlayRuntimeMarker(PlayActivation{OperationID: "legacy", Fingerprint: "reviewed", ModCount: 3, ModsPath: managed,
		UserPath: service.config.BeamNGRoot, ActivatedAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)}); err != nil { t.Fatal(err) }
	if err := service.store.AppendEvent(ctx, "", "archive_deployment_database_backup", map[string]any{"path": "backup"}); err != nil { t.Fatal(err) }

	service.modImportMu.Lock()
	err = service.adoptExistingManagedEntries(ctx)
	service.modImportMu.Unlock()
	if err != nil { t.Fatal(err) }
	owned, err := service.store.listOwnedArchiveEntries(ctx)
	if err != nil { t.Fatal(err) }
	methods := map[string]string{}
	for _, entry := range owned { methods[entry.RelativePath] = entry.Method }
	if methods[linked] != deployMethodHardlink || methods[copied] != deployMethodCopy {
		t.Fatalf("verified legacy files were not adopted correctly: %#v", methods)
	}
	if _, adopted := methods[altered]; adopted { t.Fatal("a same-named file with different bytes was adopted") }
	blockers := checkManagedRootOwnership(ctx, service.store, managed)
	if len(blockers) != 1 || !strings.Contains(blockers[0], altered) { t.Fatalf("unverified file must still block exact selection: %#v", blockers) }
	if data, err := os.ReadFile(filepath.Join(managed, altered)); err != nil || string(data) != "same legacy name, different bytes" { t.Fatal("unverified file was changed", err) }
}
