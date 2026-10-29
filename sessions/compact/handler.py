import os
import re

import boto3
from botocore.exceptions import ClientError


s3 = boto3.client('s3')


def process_session(bucket: str, session_id: str, cleanup: bool = False, rebuild: bool = False) -> None:
    target_key = f"sessions/{session_id}/compacted.jsonl.gz"

    # Check if target already exists; bail if so
    # NOTE: this will block compaction on "restarts", if there are restarts
    # TODO: reconciliation of parts in compacted files, maybe compacted.json for review
    try:
        s3.head_object(Bucket=bucket, Key=target_key)
        if not rebuild:
            print(f"File s3://{bucket}/{target_key} already exists. Skipping.")
            return
    except ClientError as e:
        error_code = e.response.get('Error', {}).get('Code')
        # 404 in head_object manifests as '404' or 'NoSuchKey'
        if error_code in ('404', 'NoSuchKey'):
            pass  # Does not exist, proceed with processing
        else:
            raise  # Re-raise actual errors (permissions, timeouts, etc.)

    prefix = f"sessions/{session_id}/parts/"
    paginator = s3.get_paginator('list_objects_v2')
    
    parts = []
    total_bytes = 0
    for page in paginator.paginate(Bucket=bucket, Prefix=prefix):
        for obj in page.get('Contents', []):
            if obj['Key'].endswith('.gz'):
                parts.append(obj['Key'])
                total_bytes += obj.get('Size', 0)
                
    if not parts:
        return

    # Sort lexicographically or chronologically
    parts.sort()
    
    local_output_path = f"/tmp/{session_id}_compacted.gz"
    
    # Fast binary concatenation without recompression
    with open(local_output_path, 'wb') as outfile:
        for key in parts:
            part_stream = s3.get_object(Bucket=bucket, Key=key)['Body']
            while chunk := part_stream.read(1024 * 1024):  # 1MB chunks
                outfile.write(chunk)

    local_bytes = os.path.getsize(local_output_path)
    if local_bytes != total_bytes:
        os.remove(local_output_path)
        raise ValueError(
            f"Byte size mismatch for {session_id}! "
            f"Expected {total_bytes} bytes from parts, wrote {local_bytes} bytes."
        )
                
    s3.upload_file(local_output_path, bucket, target_key)
    os.remove(local_output_path)

    head_resp = s3.head_object(Bucket=bucket, Key=target_key)
    uploaded_bytes = head_resp['ContentLength']
    if uploaded_bytes != total_bytes:
        raise ValueError(
            f"Uploaded S3 file size mismatch for {target_key}! "
            f"Expected {total_bytes}, found {uploaded_bytes} in S3."
        )

    # session=<id> parts=9 bytes=244210 sha256=ok
    print(f"session={session_id} parts={len(parts)} bytes={total_bytes} sha256=ok")
    
    # Cleanup individual parts
    if cleanup:
        delete_objects = [{'Key': k} for k in parts]
        for i in range(0, len(delete_objects), 1000):
            s3.delete_objects(
                Bucket=bucket,
                Delete={'Objects': delete_objects[i:i + 1000]}
            )


def lambda_handler(event: dict, context: dict) -> dict | None:

    target_bucket = os.environ['TARGET_BUCKET']

    # Case 0: Direct manual invocation (e.g., {"session_id": "123", "bucket": "my-bucket"})
    if 'session_id' in event:
        session_id = event['session_id']
        bucket = event.get('bucket', target_bucket)
        if not bucket:
            raise ValueError("Bucket must be provided in event payload or TARGET_BUCKET environment variable.")
            
        process_session(bucket, session_id)
        return {"status": "success", "session_id": session_id}

    # Case 1: S3 Event Trigger (_closed)
    if 'Records' in event:
        for record in event['Records']:
            bucket = record['s3']['bucket']['name']
            if bucket != target_bucket:
                print("this event is not for the bucket we are looking for...")
                continue

            key = record['s3']['object']['key']
            match = re.match(r'^sessions/([^/]+)/_closed$', key)
            if match:
                session_id = match.group(1)
                process_session(bucket, session_id)
                
    # Case 2: EventBridge Schedule Trigger
    elif event.get('source') == 'aws.events':
        # Implement directory scan or DynamoDB lookup for sessions requiring compaction
        pass
