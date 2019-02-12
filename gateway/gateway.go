package gateway

import (
	"bytes"
	"fmt"
	ds "gx/ipfs/QmUadX5EcvrBmxAV9sE7wUWtWSqxns5K84qKJBixmcT1w9/go-datastore"
	dsq "gx/ipfs/QmUadX5EcvrBmxAV9sE7wUWtWSqxns5K84qKJBixmcT1w9/go-datastore/query"
	"io/ioutil"
	"path"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

// NewDatastore is used to create our datastore against the minio gateway powered by storj
func NewDatastore(cfg Config) (*Datastore, error) {
	// Configure to use Minio Server
	s3Config := &aws.Config{
		// TODO: determine if we need session token
		Credentials:      credentials.NewStaticCredentials(cfg.AccessKey, cfg.SecretKey, ""),
		Endpoint:         aws.String(cfg.Endpoint),
		Region:           aws.String(cfg.Region),
		DisableSSL:       aws.Bool(cfg.Secure),
		S3ForcePathStyle: aws.Bool(true),
	}
	s3Session := session.New(s3Config)
	d := &Datastore{
		Config: cfg,
		S3:     s3.New(s3Session),
	}
	// create the bucket we want to store things in
	if err := d.CreateBucket(cfg.Bucket); err != nil {
		return nil, err
	}
	return d, nil
}

// IPFS DATASTORE FUNCTION CALLS

// Put is used to store some data
func (d *Datastore) Put(k ds.Key, value []byte) error {
	_, err := d.S3.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(d.Bucket),
		Key:    aws.String(d.s3Path(k.String())),
		Body:   bytes.NewReader(value),
	})
	return parseError(err)
}

// Get is used to retrieve data from our storj backed s3 datastore
func (d *Datastore) Get(k ds.Key) ([]byte, error) {
	resp, err := d.S3.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(d.Bucket),
		Key:    aws.String(d.s3Path(k.String())),
	})
	if err != nil {
		return nil, parseError(err)
	}
	defer resp.Body.Close()

	return ioutil.ReadAll(resp.Body)
}

// Has is used to check if we already have an object matching this key
func (d *Datastore) Has(k ds.Key) (exists bool, err error) {
	_, err = d.GetSize(k)
	if err != nil {
		if err == ds.ErrNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetSize is used to retrieve the size of an object
func (d *Datastore) GetSize(k ds.Key) (size int, err error) {
	resp, err := d.S3.HeadObject(&s3.HeadObjectInput{
		Bucket: aws.String(d.Bucket),
		Key:    aws.String(d.s3Path(k.String())),
	})
	if err != nil {
		if s3Err, ok := err.(awserr.Error); ok && s3Err.Code() == "NotFound" {
			return -1, ds.ErrNotFound
		}
		return -1, err
	}
	return int(*resp.ContentLength), nil
}

// Delete is used to remove an object from our datastore
func (d *Datastore) Delete(k ds.Key) error {
	_, err := d.S3.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(d.Bucket),
		Key:    aws.String(d.s3Path(k.String())),
	})
	return parseError(err)
}

// Query is used to examine our s3 datastore and pull any objects
// matching our given query
func (d *Datastore) Query(q dsq.Query) (dsq.Results, error) {
	if q.Orders != nil || q.Filters != nil {
		return nil, fmt.Errorf("s3ds: filters or orders are not supported")
	}

	limit := q.Limit + q.Offset
	/*	TODO: we may/may not need to add
		if limit == 0 || limit > listMax {
			limit = listMax
		}
	*/
	resp, err := d.S3.ListObjectsV2(&s3.ListObjectsV2Input{
		Bucket:  aws.String(d.Bucket),
		Prefix:  aws.String(d.s3Path(q.Prefix)),
		MaxKeys: aws.Int64(int64(limit)),
	})
	if err != nil {
		return nil, err
	}

	index := q.Offset
	nextValue := func() (dsq.Result, bool) {
		for index >= len(resp.Contents) {
			if !*resp.IsTruncated {
				return dsq.Result{}, false
			}

			index -= len(resp.Contents)

			resp, err = d.S3.ListObjectsV2(&s3.ListObjectsV2Input{
				Bucket:            aws.String(d.Bucket),
				Prefix:            aws.String(d.s3Path(q.Prefix)),
				Delimiter:         aws.String("/"),
				MaxKeys:           aws.Int64(listMax),
				ContinuationToken: resp.NextContinuationToken,
			})
			if err != nil {
				return dsq.Result{Error: err}, false
			}
		}

		entry := dsq.Entry{
			Key: ds.NewKey(*resp.Contents[index].Key).String(),
		}
		if !q.KeysOnly {
			value, err := d.Get(ds.NewKey(entry.Key))
			if err != nil {
				return dsq.Result{Error: err}, false
			}
			entry.Value = value
		}

		index++
		return dsq.Result{Entry: entry}, true
	}

	return dsq.ResultsFromIterator(q, dsq.Iterator{
		Close: func() error {
			return nil
		},
		Next: nextValue,
	}), nil
}

// S3 FUNCTION CALLS

// CreateBucket is used to create a bucket
func (d *Datastore) CreateBucket(name string) error {
	createParam := &s3.CreateBucketInput{
		Bucket: aws.String(name),
	}
	// create bucket ensure we have initialize client correct
	_, err := d.S3.CreateBucket(createParam)
	return err
}

// DeleteBucket is used to remove the specified bucket
func (d *Datastore) DeleteBucket(name string) error {
	deleteParam := &s3.DeleteBucketInput{
		Bucket: aws.String(name),
	}
	_, err := d.S3.DeleteBucket(deleteParam)
	return err
}

// HELPER FUNCTION CALLS

// TODO: not sure if we need this, borrowing from the go-s3-ds ipfs repo
func (d *Datastore) s3Path(p string) string {
	return path.Join(d.RootDirectory, p)
}

func parseError(err error) error {
	if s3Err, ok := err.(awserr.Error); ok && s3Err.Code() == s3.ErrCodeNoSuchKey {
		return ds.ErrNotFound
	}
	return nil
}
