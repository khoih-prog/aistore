#
# Copyright (c) 2021-22, NVIDIA CORPORATION. All rights reserved.
#

# pylint: disable=unused-variable

TAR2TF = "tar2tf"
OBJECTS = "objects"
START = "start"

# URL Params
QParamArchpath = "archpath"
QParamProvider = "provider"
QParamWhat = "what"
QParamKeepBckMD = "keep_md"
QParamBucketTo = "bck_to"
QparamPrimaryReadyReb = "prr"

# Bucket providers
ProviderAIS = "ais"
ProviderAmazon = "aws"
ProviderGoogle = "gcp"
ProviderHTTP = "ht"
ProviderAzure = "azure"
ProviderHDFS = "hdfs"

HTTP_METHOD_GET = "get"
HTTP_METHOD_POST = "post"
HTTP_METHOD_DELETE = "delete"
HTTP_METHOD_PUT = "put"
HTTP_METHOD_HEAD = "head"

# Actions
ACT_COPY_BCK = "copy-bck"
ACT_CREATE_BCK = "create-bck"
ACT_DESTROY_BCK = "destroy-bck"
ACT_EVICT_REMOTE_BCK = "evict-remote-bck"
ACT_LIST = "list"
ACT_MOVE_BCK = "move-bck"
ACT_ETL_BCK = "etl-bck"

# Defaults
DEFAULT_CHUNK_SIZE = 32768

# templates for ETL
CODE_TEMPLATE = """
import pickle
import base64

_base64code = base64.b64decode('{}')
transform = pickle.loads(_base64code)
{}
"""

CODE_TEMPLATE_STREAM = """
import pickle
import base64

before = pickle.loads(base64.b64decode('{}'))
transform = pickle.loads(base64.b64decode('{}'))
after = pickle.loads(base64.b64decode('{}'))
{}
"""
