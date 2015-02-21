/*
 * Mini Object Storage, (C) 2015 Minio, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package fs

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"os"
	"path"
	"strings"
	"sync"

	mstorage "github.com/minio-io/minio/pkg/storage"
	"github.com/minio-io/minio/pkg/utils/policy"
)

type storage struct {
	root string
	lock *sync.Mutex
}

type SerializedMetadata struct {
	ContentType string
}

func Start(root string) (chan<- string, <-chan error, *storage) {
	ctrlChannel := make(chan string)
	errorChannel := make(chan error)
	s := storage{}
	s.root = root
	s.lock = new(sync.Mutex)
	go start(ctrlChannel, errorChannel, &s)
	return ctrlChannel, errorChannel, &s
}

func start(ctrlChannel <-chan string, errorChannel chan<- error, s *storage) {
	err := os.MkdirAll(s.root, 0700)
	errorChannel <- err
	close(errorChannel)
}

// Bucket Operations
func (storage *storage) ListBuckets() ([]mstorage.BucketMetadata, error) {
	files, err := ioutil.ReadDir(storage.root)
	if err != nil {
		return []mstorage.BucketMetadata{}, mstorage.EmbedError("bucket", "", err)
	}

	var metadataList []mstorage.BucketMetadata
	for _, file := range files {
		// Skip policy files
		if strings.HasSuffix(file.Name(), "_policy.json") {
			continue
		}
		if !file.IsDir() {
			return []mstorage.BucketMetadata{}, mstorage.BackendCorrupted{Path: storage.root}
		}
		metadata := mstorage.BucketMetadata{
			Name:    file.Name(),
			Created: file.ModTime(), // TODO - provide real created time
		}
		metadataList = append(metadataList, metadata)
	}
	return metadataList, nil
}

func (storage *storage) StoreBucket(bucket string) error {
	storage.lock.Lock()
	defer storage.lock.Unlock()

	// verify bucket path legal
	if mstorage.IsValidBucket(bucket) == false {
		return mstorage.BucketNameInvalid{Bucket: bucket}
	}

	// get bucket path
	bucketDir := path.Join(storage.root, bucket)

	// check if bucket exists
	if _, err := os.Stat(bucketDir); err == nil {
		return mstorage.BucketExists{
			Bucket: bucket,
		}
	}

	// make bucket
	err := os.Mkdir(bucketDir, 0700)
	if err != nil {
		return mstorage.EmbedError(bucket, "", err)
	}
	return nil
}

func (storage *storage) GetBucketPolicy(bucket string) (interface{}, error) {
	storage.lock.Lock()
	defer storage.lock.Unlock()

	var p policy.BucketPolicy
	// verify bucket path legal
	if mstorage.IsValidBucket(bucket) == false {
		return policy.BucketPolicy{}, mstorage.BucketNameInvalid{Bucket: bucket}
	}

	// get bucket path
	bucketDir := path.Join(storage.root, bucket)
	// check if bucket exists
	if _, err := os.Stat(bucketDir); err != nil {
		return policy.BucketPolicy{}, mstorage.BucketNotFound{Bucket: bucket}
	}

	// get policy path
	bucketPolicy := path.Join(storage.root, bucket+"_policy.json")
	filestat, err := os.Stat(bucketPolicy)
	if filestat.IsDir() {
		return policy.BucketPolicy{}, mstorage.BackendCorrupted{Path: bucketPolicy}
	}

	if os.IsNotExist(err) {
		return policy.BucketPolicy{}, mstorage.BucketPolicyNotFound{Bucket: bucket}
	}

	file, err := os.OpenFile(bucketPolicy, os.O_RDONLY, 0666)
	defer file.Close()
	if err != nil {
		return policy.BucketPolicy{}, mstorage.EmbedError(bucket, "", err)
	}
	encoder := json.NewDecoder(file)
	err = encoder.Decode(&p)
	if err != nil {
		return policy.BucketPolicy{}, mstorage.EmbedError(bucket, "", err)
	}

	return p, nil

}

func (storage *storage) StoreBucketPolicy(bucket string, policy interface{}) error {
	storage.lock.Lock()
	defer storage.lock.Unlock()

	// verify bucket path legal
	if mstorage.IsValidBucket(bucket) == false {
		return mstorage.BucketNameInvalid{Bucket: bucket}
	}

	// get bucket path
	bucketDir := path.Join(storage.root, bucket)
	// check if bucket exists
	if _, err := os.Stat(bucketDir); err != nil {
		return mstorage.BucketNotFound{
			Bucket: bucket,
		}
	}

	// get policy path
	bucketPolicy := path.Join(storage.root, bucket+"_policy.json")
	filestat, _ := os.Stat(bucketPolicy)
	if filestat.IsDir() {
		return mstorage.BackendCorrupted{Path: bucketPolicy}
	}
	file, err := os.OpenFile(bucketPolicy, os.O_WRONLY|os.O_CREATE, 0600)
	defer file.Close()
	if err != nil {
		return mstorage.EmbedError(bucket, "", err)
	}
	encoder := json.NewEncoder(file)
	err = encoder.Encode(policy)
	if err != nil {
		return mstorage.EmbedError(bucket, "", err)
	}
	return nil
}

// Object Operations

func (storage *storage) CopyObjectToWriter(w io.Writer, bucket string, object string) (int64, error) {
	// validate bucket
	if mstorage.IsValidBucket(bucket) == false {
		return 0, mstorage.BucketNameInvalid{Bucket: bucket}
	}

	// validate object
	if mstorage.IsValidObject(object) == false {
		return 0, mstorage.ObjectNameInvalid{Bucket: bucket, Object: object}
	}

	objectPath := path.Join(storage.root, bucket, object)

	filestat, err := os.Stat(objectPath)
	switch err := err.(type) {
	case nil:
		{
			if filestat.IsDir() {
				return 0, mstorage.ObjectNotFound{Bucket: bucket, Object: object}
			}
		}
	default:
		{
			if os.IsNotExist(err) {
				return 0, mstorage.ObjectNotFound{Bucket: bucket, Object: object}
			} else {
				return 0, mstorage.EmbedError(bucket, object, err)
			}
		}
	}
	file, err := os.Open(objectPath)
	defer file.Close()
	if err != nil {
		return 0, mstorage.EmbedError(bucket, object, err)
	}
	count, err := io.Copy(w, file)
	if err != nil {
		return count, mstorage.EmbedError(bucket, object, err)
	}
	return count, nil
}

func (storage *storage) GetObjectMetadata(bucket string, object string) (mstorage.ObjectMetadata, error) {
	if mstorage.IsValidBucket(bucket) == false {
		return mstorage.ObjectMetadata{}, mstorage.BucketNameInvalid{Bucket: bucket}
	}

	if mstorage.IsValidObject(bucket) == false {
		return mstorage.ObjectMetadata{}, mstorage.ObjectNameInvalid{Bucket: bucket, Object: bucket}
	}

	objectPath := path.Join(storage.root, bucket, object)

	stat, err := os.Stat(objectPath)
	if os.IsNotExist(err) {
		return mstorage.ObjectMetadata{}, mstorage.ObjectNotFound{Bucket: bucket, Object: object}
	}

	_, err = os.Stat(objectPath + "$metadata")
	if os.IsNotExist(err) {
		return mstorage.ObjectMetadata{}, mstorage.ObjectNotFound{Bucket: bucket, Object: object}
	}

	file, err := os.Open(objectPath + "$metadata")
	defer file.Close()
	if err != nil {
		return mstorage.ObjectMetadata{}, mstorage.EmbedError(bucket, object, err)
	}

	metadataBuffer, err := ioutil.ReadAll(file)
	if err != nil {
		return mstorage.ObjectMetadata{}, mstorage.EmbedError(bucket, object, err)
	}

	var deserializedMetadata SerializedMetadata
	err = json.Unmarshal(metadataBuffer, &deserializedMetadata)
	if err != nil {
		return mstorage.ObjectMetadata{}, mstorage.EmbedError(bucket, object, err)
	}

	contentType := "application/octet-stream"
	if deserializedMetadata.ContentType != "" {
		contentType = deserializedMetadata.ContentType
	}
	contentType = strings.TrimSpace(contentType)

	metadata := mstorage.ObjectMetadata{
		Bucket:      bucket,
		Key:         object,
		Created:     stat.ModTime(),
		Size:        stat.Size(),
		ETag:        bucket + "#" + object,
		ContentType: contentType,
	}

	return metadata, nil
}

func (storage *storage) ListObjects(bucket, prefix string, count int) ([]mstorage.ObjectMetadata, bool, error) {
	if mstorage.IsValidBucket(bucket) == false {
		return []mstorage.ObjectMetadata{}, false, mstorage.BucketNameInvalid{Bucket: bucket}
	}
	if mstorage.IsValidObject(prefix) == false {
		return []mstorage.ObjectMetadata{}, false, mstorage.ObjectNameInvalid{Bucket: bucket, Object: prefix}
	}

	rootPrefix := path.Join(storage.root, bucket)

	// check bucket exists
	if _, err := os.Stat(rootPrefix); os.IsNotExist(err) {
		return []mstorage.ObjectMetadata{}, false, mstorage.BucketNotFound{Bucket: bucket}
	}

	files, err := ioutil.ReadDir(rootPrefix)
	if err != nil {
		return []mstorage.ObjectMetadata{}, false, mstorage.EmbedError("bucket", "", err)
	}

	var metadataList []mstorage.ObjectMetadata
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), "$metadata") {
			if len(metadataList) >= count {
				return metadataList, true, nil
			}
			if strings.HasPrefix(file.Name(), prefix) {
				metadata := mstorage.ObjectMetadata{
					Bucket:  bucket,
					Key:     file.Name(),
					Created: file.ModTime(),
					Size:    file.Size(),
					ETag:    bucket + "#" + file.Name(),
				}
				metadataList = append(metadataList, metadata)
			}
		}
	}
	return metadataList, false, nil
}

func (storage *storage) StoreObject(bucket, key, contentType string, data io.Reader) error {
	// TODO Commits should stage then move instead of writing directly
	storage.lock.Lock()
	defer storage.lock.Unlock()

	// check bucket name valid
	if mstorage.IsValidBucket(bucket) == false {
		return mstorage.BucketNameInvalid{Bucket: bucket}
	}

	// check bucket exists
	if _, err := os.Stat(path.Join(storage.root, bucket)); os.IsNotExist(err) {
		return mstorage.BucketNotFound{Bucket: bucket}
	}

	// verify object path legal
	if mstorage.IsValidObject(key) == false {
		return mstorage.ObjectNameInvalid{Bucket: bucket, Object: key}
	}

	// verify content type
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	contentType = strings.TrimSpace(contentType)

	// get object path
	objectPath := path.Join(storage.root, bucket, key)
	objectDir := path.Dir(objectPath)
	if _, err := os.Stat(objectDir); os.IsNotExist(err) {
		err = os.MkdirAll(objectDir, 0700)
		if err != nil {
			return mstorage.EmbedError(bucket, key, err)
		}
	}

	// check if object exists
	if _, err := os.Stat(objectPath); !os.IsNotExist(err) {
		return mstorage.ObjectExists{
			Bucket: bucket,
			Key:    key,
		}
	}

	// write object
	file, err := os.OpenFile(objectPath, os.O_WRONLY|os.O_CREATE, 0600)
	defer file.Close()
	if err != nil {
		return mstorage.EmbedError(bucket, key, err)
	}

	_, err = io.Copy(file, data)
	if err != nil {
		return mstorage.EmbedError(bucket, key, err)
	}

	// serialize metadata to json

	metadataBuffer, err := json.Marshal(SerializedMetadata{ContentType: contentType})
	if err != nil {
		return mstorage.EmbedError(bucket, key, err)
	}

	//
	file, err = os.OpenFile(objectPath+"$metadata", os.O_WRONLY|os.O_CREATE, 0600)
	defer file.Close()
	if err != nil {
		return mstorage.EmbedError(bucket, key, err)
	}

	_, err = io.Copy(file, bytes.NewBuffer(metadataBuffer))
	if err != nil {
		return mstorage.EmbedError(bucket, key, err)
	}

	return nil
}