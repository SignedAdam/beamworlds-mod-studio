package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func syncReviewedCollectionFolder(t *testing.T,service *AppService,id string) CollectionFolder {
	t.Helper()
	plan,err:=service.PlanCollectionFolder(context.Background(),id)
	if err!=nil { t.Fatal(err) }
	folder,err:=service.syncCollectionFolder(context.Background(),id,plan.Fingerprint,true)
	if err!=nil { t.Fatal(err) }
	return folder
}

func ownedMirrorPath(t *testing.T,service *AppService,collectionID,entityID string) string {
	t.Helper()
	entries,err:=service.store.listOwnedArchiveEntries(context.Background())
	if err!=nil { t.Fatal(err) }
	for _,entry:=range entries {
		if entry.Purpose==archivePurposeCollection && entry.OwnerID==collectionID && entry.EntityID==entityID && entry.State==archiveStateActive {
			return filepath.Join(entry.TargetRoot,entry.RelativePath)
		}
	}
	t.Fatalf("no owned mirror for %s in %s",entityID,collectionID)
	return ""
}

func TestSyncCollectionFolderMirrorsResolvedMods(t *testing.T) {
	service:=newTestAppService(t)
	items,collectionID:=scanAndCreateCollection(t,service,"Export Me",3,9100)
	folder:=syncReviewedCollectionFolder(t,service,collectionID)
	if folder.ModCount!=3 || folder.CopiedCount!=0 { t.Fatalf("mirror counts = %+v",folder) }
	if filepath.Base(folder.Path)!="Export-Me" { t.Fatalf("folder path = %s",folder.Path) }
	for _,item:=range items {
		mirrored:=ownedMirrorPath(t,service,collectionID,item.EntityID)
		actual,err:=os.Stat(mirrored);if err!=nil {t.Fatal(err)}
		source,err:=os.Stat(item.ArchivePath);if err!=nil {t.Fatal(err)}
		if !os.SameFile(actual,source) {t.Fatalf("%s does not share its source archive",mirrored)}
	}
}

func TestCollectionMembershipRemovalRetiresMirrorImmediately(t *testing.T) {
	service:=newTestAppService(t)
	items,collectionID:=scanAndCreateCollection(t,service,"Trim",2,9200)
	first:=syncReviewedCollectionFolder(t,service,collectionID)
	removed:=ownedMirrorPath(t,service,collectionID,items[0].EntityID)
	kept:=ownedMirrorPath(t,service,collectionID,items[1].EntityID)
	if _,err:=service.SetCollectionMods(collectionID,[]string{items[0].EntityID},false);err!=nil {t.Fatal(err)}
	if _,err:=os.Stat(removed);!os.IsNotExist(err) {t.Fatalf("removed member retained a generated alias: %v",err)}
	if _,err:=os.Stat(kept);err!=nil {t.Fatal("remaining member lost its alias",err)}
	if _,err:=os.Stat(items[0].ArchivePath);err!=nil {t.Fatal("membership removal touched canonical archive",err)}
	second:=syncReviewedCollectionFolder(t,service,collectionID)
	if second.Path!=first.Path || second.ModCount!=1 || second.AddedCount!=0 {t.Fatalf("idempotent mirror refresh = %+v",second)}
}

func TestCollectionMirrorPreservesForeignFilesAcrossRefreshAndRename(t *testing.T) {
	service:=newTestAppService(t)
	items,collectionID:=scanAndCreateCollection(t,service,"Before",1,9300)
	before:=syncReviewedCollectionFolder(t,service,collectionID)
	oldAlias:=ownedMirrorPath(t,service,collectionID,items[0].EntityID)
	for _,name:=range []string{"notes.txt","personal.zip"} {
		if err:=os.WriteFile(filepath.Join(before.Path,name),[]byte("user-owned"),0600);err!=nil {t.Fatal(err)}
	}
	syncReviewedCollectionFolder(t,service,collectionID)
	if _,err:=service.UpdateCollection(collectionID,"After","");err!=nil {t.Fatal(err)}
	after:=syncReviewedCollectionFolder(t,service,collectionID)
	if filepath.Base(after.Path)!="After" {t.Fatalf("renamed path = %s",after.Path)}
	for _,name:=range []string{"notes.txt","personal.zip"} {
		data,err:=os.ReadFile(filepath.Join(before.Path,name));if err!=nil || string(data)!="user-owned" {t.Fatalf("foreign %s changed: %s %v",name,data,err)}
	}
	if _,err:=os.Stat(oldAlias);!os.IsNotExist(err) {t.Fatalf("stale owned alias survived rename: %v",err)}
	if _,err:=os.Stat(ownedMirrorPath(t,service,collectionID,items[0].EntityID));err!=nil {t.Fatal(err)}
}

func TestCollectionMirrorRefusesUnownedDestinationCollision(t *testing.T) {
	service:=newTestAppService(t)
	_,collectionID:=scanAndCreateCollection(t,service,"Collision",1,9400)
	plan,err:=service.PlanCollectionFolder(context.Background(),collectionID);if err!=nil {t.Fatal(err)}
	path:=plan.Entries[0].DestinationPath
	if err:=os.MkdirAll(filepath.Dir(path),0755);err!=nil {t.Fatal(err)}
	if err:=os.WriteFile(path,[]byte("unowned archive"),0600);err!=nil {t.Fatal(err)}
	if _,err:=service.syncCollectionFolder(context.Background(),collectionID,plan.Fingerprint,true);err==nil {t.Fatal("unowned destination was overwritten")}
	data,err:=os.ReadFile(path);if err!=nil || string(data)!="unowned archive" {t.Fatalf("unowned data changed: %s, %v",data,err)}
}

func TestCollectionCopyMirrorReusesArchiveOnRename(t *testing.T) {
	service:=newTestAppService(t)
	service.config.ArchiveDeploymentMode=DeploymentModeCopy
	items,collectionID:=scanAndCreateCollection(t,service,"Copy Before",1,9500)
	syncReviewedCollectionFolder(t,service,collectionID)
	oldPath:=ownedMirrorPath(t,service,collectionID,items[0].EntityID)
	before,err:=os.Stat(oldPath);if err!=nil {t.Fatal(err)}
	source,err:=os.Stat(items[0].ArchivePath);if err!=nil {t.Fatal(err)}
	if os.SameFile(before,source) {t.Fatal("Copies mode shared the canonical archive")}
	if _,err:=service.UpdateCollection(collectionID,"Copy After","");err!=nil {t.Fatal(err)}
	after:=syncReviewedCollectionFolder(t,service,collectionID)
	if after.CopiedCount!=0 {t.Fatal("rename recopied unchanged archive payload")}
	newPath:=ownedMirrorPath(t,service,collectionID,items[0].EntityID)
	moved,err:=os.Stat(newPath);if err!=nil {t.Fatal(err)}
	if !os.SameFile(before,moved) {t.Fatal("rename did not reuse the original independent deployment copy")}
}
