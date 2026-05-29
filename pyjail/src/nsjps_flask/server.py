"""
Flask server for running programming assignments using nsjail.

### Testing

To test it out, run:
```
python server.py
```
and go to `http://localhost:5000`

### Production
To run this in production:

1. Run the gunicorn server. Refer to wsgi.py for instructions on that.
2. After running the gunicorn server, copy `nginx.conf` to
`/etc/nginx.sites-enabled/nsjail.conf`
3. Restart nginx
4. Go to http://localhost
5. Configure nginx as required.
"""

import logging
from datetime import datetime

from flask import Flask
from flask_restful import Api, Resource, reqparse
from google.protobuf.text_format import Merge, MessageToString

from code_evaluator import code_manager
from proto import request_pb2


now = datetime.now

app = Flask(__name__)
api = Api(app)


class ProgrammingServer(Resource):
    """Server handling requests for running programs"""

    def __init__(self):
        """Initializes the required POST params, etc"""
        self.reqparse = reqparse.RequestParser()
        self.reqparse.add_argument(
            "message",
            type=str,
            required=True,
            help="No data provided",
            location="json",
        )

    def get(self):
        """GET endpoint for health check"""
        return "OK"

    def post(self):
        """POST endpoint to run a programming question"""
        args = self.reqparse.parse_args()
        # Encode into ASCII to remove bad chars
        message = args["message"].encode("ascii", "ignore").decode("ascii")
        request_obj = Merge(message, request_pb2.CodeRequest())
        del args
        reply = request_pb2.CodeReply(
            compilation_result=request_pb2.CodeReply.CompilationResult(
                status=request_pb2.CodeReply.NOT_RUN,
            ),
            overall_status=request_pb2.CodeReply.NOT_RUN,
        )
        # Test editing by swami
        if request_obj.evaluation_script:
            code_manager_obj = code_manager.EvaluationScriptCodeManager(
                request_obj,
                reply,
            )
        else:
            code_manager_obj = code_manager.ThreadingCodeManager(
                request_obj,
                reply,
            )
        try:
            code_manager_obj.run_all()
        except Exception as e:
            logging.info("Program run failed. Cleaning up.")
            logging.debug(f"Exception: {e}")
        code_manager_obj.cleanup()
        #        import json
        #        td={'score':50,'comment':'This is test'}
        #        reply.evaluation_result_json=json.dumps(td)
        return {"message": MessageToString(reply)}


@app.route("/public_settings/")
def get_public_settings():
    """GET endpoint which returns an ASCII dump of the public settings"""
    return MessageToString(code_manager.PUBLIC_SETTINGS)


api.add_resource(ProgrammingServer, "/")

if __name__ == "__main__":
    # TO be used by bazel only. Do not run this file manually, will result
    # in errors.
    app.run(debug=True)
